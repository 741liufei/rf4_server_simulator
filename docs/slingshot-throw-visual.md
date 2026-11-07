# 弹弓抛饵视觉链路（805 / 13-5）现场记录

状态：服务端侧能改的字段全部实测完，飞行方向仍不对。下一步进客户端逆向。
本文是把「这个视觉到底怎么被调用、已经证明到什么程度」固定下来，避免下一轮重复踩。

## 1. 调用链（实机时序，2026-11-07 上午实测）

    13/4  拿起弹弓（手持物）
    13/1  整堆装填：来源堆删除、新子实例挂到弹弓 rig 上，f1=csHandLoaded 记装填饵堆 guid
    13/5  每抛一发：请求 [手持物GUID, obj19 olieodeoaof]，服务端消耗弹药格 1 份，回 805 lmbomkliaad
    13/6  全量快照 800（无落点坐标）

弹药格里那一格才是被扣的那一格，背包只是弹药来源。实机 8 发 = 弹药实例 60→52，
左上角读数与库内数量一致。

代码入口：`internal/gameserver/protocol_dynamic_handlers.go` 的 `hDynamicHanditemThrow`。

## 2. 响应 805 `lmbomkliaad` 六个字段（wire_spec 权威语义）

| 字段 | 名 | 语义 | 我们现在发什么 |
|---|---|---|---|
| f0 | fipljlpnfmc | 抛投距离（米），<=0 客户端跳过建打窝点 | 客户端 f0（恒 10.0），可被 `handitem_throw_dist_force` 覆盖 |
| f1 | nomhkpfieej | 散布百分比 | `handitem_throw_scatter`，实测 3.0 最佳 |
| f2 | emgkjccefbc | 姿态块（float×5 + long FILETIME + int） | `buildPose308`：体力 0.85/0.9946/0/1/0/11.375/0 + 世界钟 |
| f3 | iingicaldbk | 物品实例数组（背包增量） | 扣料后的弹药堆 upsert |
| f4 | pbhkmflbgfb | 物品实例数组，长度 0 时客户端跳过 | 缺省空；=1 时多塞一个抛出物实例 |
| f5 | iaimladeglm | GUID 移除数组 | 弹药耗尽时移除 |

**没有世界坐标，也没有角度。** 这是「方向改不了」的根因所在。

## 3. 请求 13/5 `olieodeoaof`(obj19) 十二字段实测

    f0=10.0  f1=10.0  f2=0  f3=null  f4=0  f5=0  f6=0  f7=0  f8=0
    f9=累计浮点（617.3 → 620.9 → 633.6 → 917.0，随时间增长）
    f10=obj(deggnlnohoh/325){f0=0 f1=0 f2=0 f3=0 f4=0}   <- 整块全零
    f11=0

- 力度 44/65/90 三发，包体一模一样：力度不进请求。
- f10 的类型 325 `deggnlnohoh` 五个 float 实测范围 = x[0,366.961]、y[-0.028,0.604]、
  z[0,462.295]、f3[-178.125,179.75]、f4[-48.125,44.875] → 形状就是「坐标+偏航+俯仰」，
  但客户端发的是全零。
- 3/9 位置同步（类型 309 `onbjjlidapg`）只有 x/y/z + 水位 + 累计浮点，也没有朝向；
  服务端已存 `st.PosX/PosZ`（handlers_game.go）与 `st.CastPosX/CastPosZ`。

## 4. f2 到底是姿态块还是坐标块：已定案 = 姿态块

2026-11-07 14:10 实机单变量实验（`handitem_throw_pose_pos=1`）：把落点坐标写进
f2 前三个 float（242.17 / 0 / 423.79），结果

1. 飞行方向没有任何变化；
2. **抛两下之后玩家出现晕眩（脱力）表现。**

f2 第一位就是体力值（正常 0.85），被写成 242/0 之后客户端判定体力耗尽。
这是客户端自己的反应，比任何静态推断都硬：f2 只被当姿态/体力消费，不当坐标消费。
开关已回 0。

## 5. 已做过的实验（都是实机单变量）

| 实验 | 结果 |
|---|---|
| `handitem_throw_scatter` 0% / 5% | 五颗球粘成一坨 |
| `handitem_throw_scatter` 3% | 能散开，已定为缺省 |
| `handitem_throw_scatter` 20% | 散到看不见 |
| `handitem_throw_dist_force=60` | 四发距离都变 60，方向照旧不对 → 距离管不了方向 |
| `handitem_throw_projectile=1` | 出现两坨（客户端自己会画），缺省已关 |
| `handitem_throw_pose_pos=1` | 方向不变 + 玩家晕眩 → f2 是姿态块 |
| 61 个 Frida 钩子（`lmbomkliaad` 方法、`FeedBall` callees 等） | 两次投掷一次都没触发 → 805 走通用序列化器，DTO 方法不被调用；飞的那坨也不是 `FeedBall` 实例 |

## 6. 目前能下的结论（各 ≥2 条证据）

1. 805 载荷里没有任何能表达落点/朝向的字段，服务端只能控制距离与散布。
2. 客户端把打窝点建在「世界原点附近」：玩家 (242.17, 423.79)，朝原点的向量与 -z 轴
   夹 29.7°，与少爷观测的「空中转向正右面 / 固定向南偏」吻合；`f0<=0 时客户端根本
   不建打窝点`（也就是最早「没有饵料飞出去」的原因），说明这一坨确实是 805 触发建的。
3. 飞行方向由客户端自己算，输入不是 805 给的 → 必须进客户端代码找它到底读哪儿。

## 7. 下一步：客户端逆向待答问题

1. 客户端收到 805 后，是哪个方法建的打窝点/飞行饵？（f0/f1 的读取点）
2. 它给的落点取自哪里：本地预测、`st` 里的某个全局、还是另一条服务端推送？
3. 若是另一条推送：那条消息的类型 id 与字段布局是什么（我们没发过 = 缺失的下发口）。
4. 客户端 13/5 的 f10 位置块为什么是全零：什么条件下它才会填真值？

可用材料：

- `D:\俄罗斯钓鱼4\Game\GameAssembly.dll`（131,901,440 字节，IL2CPP x64）
- 已分析好的 Ghidra 工程：`C:\rf4_rea_ghidra_project\rf4-il2cpp`（2026-11-04 12:38 建、
  13:46 完成，内含 GameAssembly.dll，FILE_ID c0a8a8ea91c377261463266400）→ 别重复跑全量分析
- REA 已确认可用 Ghidra 12.1.2（`D:\tools\ghidra\ghidra_12.1.2_PUBLIC`，JDK 21）
- IDA 9.3sp2 + Hex-Rays：`D:\ida逆向\ida93sp2`（`idat.exe`、`hv.exe`、有 idapro.hexlic）
- RVA→类名表：`rf4_launcher_re\_methodmap.txt`（方法名是 ?，需要在运行时从 Il2CppClass 取名字）
  类名字符串堆：`rf4_launcher_re\_strheap.txt`（`RF4.Client.FishingScene|FeedBall` 这类条目）
- Frida 17.24.3 探针样板：`rf4_launcher_re\_fr_throw2.py` + `_fr_throw.js`，
  停止哨兵 = 同目录 `_fr_stop` 文件；硬杀 python 会把游戏带崩

## 8. 现场参数与回退

tunables（`data/rf4server.db`，30 秒缓存，改库不用重启）：

    handitem_throw_pose=1  pose_pos=0  projectile=0  scatter=3.0
    dist_force=0  debug=1  selfclean=0  land_cell=0  fallback_yaw=0

代码侧新增：

- `handitemThrowLanding` / `throwLandingPose308`：按「玩家位置 + 力度距离 + 玩家→浮标方向」
  算真实落点；`handitem_throw_land_cell=1` 时窝账格子改用这个落点（缺省 0，
  窝账仍记浮标那一格，弹弓饵算进钓点，这是当前对 gameplay 更有利的口径）。
- 测试：`TestDynamicHanditemThrowLandingOverridesPoseBlock`（含兜底偏航、关掉后退回姿态块）、
  `TestDynamicHanditemThrowLandingCell`；`internal/gameserver` 全量回归 238.9s 全绿。

## 9. 窝料账本不受影响（重要）

窝料投放通道 `catapult` 照常：每发 1 份、幂等键
`catapult:<弹弓短GUID>:<会话号>:<CallID>`（CallID 每次登录重新编号，不带会话号必撞键）。
视觉没修好之前，gameplay 口径已经是对的：饵记到浮标落点格，抛过竿后 cell=53,85（真实格）。

## 10. 客户端侧定案（2026-11-07 15:20，Frida 活钩子 + Ghidra 双向互证）

**一句话：805 整条消息里没有落点字段。抛饵方向是客户端自己算的，服务端没有任何可下发的数据能修正它。**

### 证据一：客户端自己的读取函数（不是猜的）

`FUN_180aab070`（rva 0xaab070，= 805 body 的读取器）读序：

| 读序 | 写入对象偏移 | 类型 |
|---|---|---|
| 1 | +0x34 | float 抛投距离 |
| 2 | +0x30 | float 散布百分比 |
| 3 | +0x28 | 嵌套 pose（`pmdcpeegbbd`） |
| 4 | +0x18 | 嵌套 X2 |
| 5 | +0x20 | 嵌套 X2（同类型） |
| 6 | +0x10 | 嵌套 X3 |

X2/X3 由 805 处理器调用的 `eegjkjenhoe`（结算基座 `pcnefbpgdhl`）消费 = 物品实例数组 / Guid 移除数组，
和 `internal/wiregen/tables.go` 里 `lmbomkliaad`→`hfenblpnihn` 的 6 字段 schema 逐条对上：
`距离 / 散布 / 姿态块 / 物品实例数组 / 物品实例数组 / Guid移除数组`。**没有位置，也没有朝向。**
写入端 `FUN_180aaaf40`（rva 0xaaaf40）同样只有这 6 项，双向一致。

### 证据二：活钩子实测（PID 37696，15:11 / 15:20 两组）

- 805 到达时 payload：`dist=10.000`、`scatter=3.000` → **我们下发的值一条不差到位**
- 805 处理器 `this+0x10` = 16 字节 GUID `e3dde45a-698a-4234-20c0-be1e1ea10ab5`，即日志里的 `hand=e3dde45a`
- 抛饵协程参数**只有三样**：`+0x10=距离`、`+0x14=散布`、`+0x28..0x34=GUID`。
  Ghidra 早先反编译里那四个"从 this 拷来的 float"其实就是这个 GUID 的 16 个字节，不是坐标
- 顺序：`H805 → SPAWN` 严格相邻（两次都是），不存在"客户端松手先抛一次、我们再多抛一次"
- 客户端实时瞄准块（`FUN_180799f00` onLeave）：`x=265.055 / yaw=-3.433 / y=0.182 / z=422.260 / pitch=+12.375`
  —— 这块是活的、跟着人物朝向实时刷新的，瞄准本身没问题
- **可是 13/5 请求里客户端自己上报的位置块（type 325 `deggnlnohoh` = DTO `pohcnpafkha`）五个 float 全是 0**
- 发射那一刻 `Cobra.cijkijmigci(FeedLoad)`（rva 0x548600）里 FeedLoad 的 `+0x10..0x2c` 全是 0
  → 饵团带着**空目标**起飞 → 被制导往 `(0,0,0)` = 世界原点
  → 人在 265/422 面朝东，原点在他的右前方 → 就是少爷看到的"飞出去后空中转向正右面 / 往 S 飞"

### 瞄准块生产者 `FUN_180799f00` 完整还原

```c
p[0] = A.x - B.x;      p[1] = aimObj + 0xbc;   // yaw
p[2] = A.y;            p[3] = A.z - B.z;       p[4] = aimObj + 0xc0; // pitch
```
A = `FUN_180007750(buf, 0, classRef)`（按类型取场景对象），B = `FUN_18078f500(buf, 0)`；
闸门 `**(static 0x1840be5b8 + 0xb8) + 0x88` 实测非空（对象 0x1c3ff81d480）。

### 所以"要传什么数据"的答案

**没有可传的数据。** 落点是客户端内部状态，它在我们的服务端环境里是空的。三条出路：

1. **接受原生视觉**（gameplay 已经对：每发 1 份、记到落点格、弹药格扣数正确）—— 缺省就这样
2. **客户端补丁**：发射瞬间（`Cobra.cijkijmigci` / `FeedLoad` 工厂）用 Frida 或服务端下发的落点
   把 FeedLoad 的目标字段写掉 —— 技术上我们已经够料做（落点、yaw 都算好了）
3. **对照原生服务端**：若官方服务端在线，用它打一发看方向是否正确；正确 = 还缺某条别的下发消息，
   可继续补；不正确 = 游戏本身的怪癖

探针与逆向料：抛饵目标点探针脚本（Frida，写 `_fr_target_stop` 哨兵文件即干净退出）与
对应的 IL2CPP Ghidra 工程，都在本机逆向目录，未随仓库发布。
Frida 17 的坑：`Interceptor.attach` 的 `onEnter` 第一个参数是**参数数组**，寄存器必须走 `this.context.rcx`，
读错就是每发都 `RangeError: invalid array index`（今天在这上面耗了三轮）。
