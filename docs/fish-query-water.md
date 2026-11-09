# 查鱼水陆粗筛

这是可选的本地预处理：**无需启动或登录游戏**，从已安装客户端的场景数值生成水陆表，查鱼时直接查表。不改变实际发鱼、落水判断、鱼种权重或玩家数据。

## 生成与使用

工具需要 Python 3.10+、[UnityPy 1.25.4](https://github.com/K0lb3/UnityPy)（MIT）和 [NumPy](https://numpy.org/)（BSD-3-Clause）。它们只用于可选的离线工具，不是 Go 服务的运行依赖；工具未复制第三方实现。下面从仓库根目录执行，替换客户端路径占位符：

```powershell
python -m venv ref/water-env
./ref/water-env/Scripts/python.exe -m pip install UnityPy==1.25.4 numpy
./ref/water-env/Scripts/python.exe tools/fish_query_water.py --client-data '<客户端目录>/rf4_x64_Data' --output ref/fish_query_water.json --audit ref/water-grid-audit.json
```

默认处理查鱼支持表内的全部内陆地图。可在命令末尾附场景标识只生成指定图；该输出仅包含指定图，不会与旧表合并。审计文件记录各图总格数、保留/剔除数、使用的水体类型和输出文件 SHA256。无法识别的地图列入 `unsupported`，不会生成全陆或全水的占位表。

生成文件只留本地，不随仓库、PR 或安装包分发；也不提交客户端图片、网格、完整高度图或资源文件。工具只读客户端目录，输出必须在安装目录以外。客户端更新、地图标定或取样规则改变后，应重新生成。

使用时另行将 `fish_query_water.json` 复制到**游戏服务工作目录的 `data/`**，然后重启游戏服务；生成工具不会自动部署、重启服务或操作客户端。也可通过 `RF4_FISH_QUERY_WATER_FILE` 指定文件。未设置时依次检查工作目录的 `data/`、`server_data/`，以及程序目录的 `data/` 和其上一级的 `server_data/`。

数据首次查询时加载，并在进程中缓存。默认路径均不存在或表中缺少本图时，沿用原区域查询，计算结果的 `WaterFiltered` 为 false，`Notes` 记录“缺少本图水陆数据”；游戏帮助统一说明该规则，常规回复不重复提示。显式指定的文件不存在、数据损坏或范围与源码不匹配时，仍返回数据无效提示，不静默错用数据。海图、手动坐标、关闭鱼群分布的查询不使用此表。

## 五点规则

1. 沿用 [查鱼坐标换算](fish-query-coordinates.md) 的地形与纸地图交集，以及 `floor(世界X/40), floor(世界Z/40)` 网格。
2. 每个裁剪后格子取中心、左下、右下、左上、右上。上边界角点向格内偏移至多百万分之一米，避免落进相邻格；中心不随角点内缩改变。
3. 点位须在识别出的水体范围内，且地形高度低于基准水面，才记为水。
4. **任一点为水就保留整格，五点均非水才剔除。** 水陆过滤先于无饵区域粗筛，不移动格子、不重新编号，不修改保留格内的概率。

地形按双线性高度采样、叠加世界 Y 原点；重叠 Terrain 取最高表面，避免将岛屿当作底层水下地形。当前高度编码会与独立的高度极值核对，无法识别时拒绝该图。Terrain 尺寸不再次应用对象缩放或旋转。

水体采用基准水面、触发范围及适用的平面水面网格。Water4 额外检查水面三角网格覆盖；Water2 与 Crest 接口采用触发范围和地形高度近似。仅采用活动水体，或活动水体控制器所引用的正常/回退水体，不把任意停用对象当作水域。

## 数据格式

顶层包含 `schema_version: 1`、`cell_size: 40`、`sampling: "center_corners_any"` 和按场景标识索引的 `maps`。每张图存：

- `bounds`：裁剪后的世界 `[minX,minZ,maxX,maxZ]`。
- `min_x`、`min_z`：最小世界网格编号；`width`、`height`：网格数量。
- `samples`：X 外层、Z 内层顺序排列。索引为 `(ix-min_x)*height+(iz-min_z)`。
- 每格整数 `0..31` 的五个位依次表示中心、左下、右下、左上、右上。`0` 剔除，非零保留；`31` 表示五点都有水，不代表全格都是水。

服务端校验格式、取样规则、格数、范围与当前扫描网格，最大每轴76格。输出中的生成时间和 `unsupported` 用于本地诊断，不参与概率计算。

## 限制与验证

这是排除明显陆地的粗筛，不是完整客户端物理判断。岸边混合格会保留，格中心仍可能在陆地；细窄水道或小水面可能被五点漏掉。未模拟波浪、潮汐、Crest 裁剪特效、地形洞、岩石、桥梁、建筑和可达抛距，也不把实际水深带入区域概率。含未支持地形洞的地图直接列为不支持。

合成数据测试不需要客户端资源：

```powershell
./ref/water-env/Scripts/python.exe -m unittest discover -s tools -p test_fish_query_water.py -v
go test ./internal/gameserver -run '^TestFishQuery' -count=1
```

实际地图生成结果和离线查询复算不等于逐图实机验收。游戏内发送“帮助”可查看五点水陆粗筛的规则和限制；常规查询不重复说明，也不宣称全格为水或可达。
