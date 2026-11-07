const {test}=require('node:test');
const assert=require('node:assert/strict'),fs=require('node:fs'),path=require('node:path');
const {chromium}=require('playwright');
const html=fs.readFileSync(path.join(__dirname,'web/index.html'),'utf8');
// 服务端对 /api/me 与 /api/dashboard 下发同一份检测结果（同一 UpdateInfo 指针）。
// 服务端对 /api/me 与 /api/dashboard 下发同一份检测结果（同一 UpdateInfo 指针）。
const MIRROR='https://gh-proxy.com/https://github.com/glance522/rf4_server_simulator/releases/download/v1.2.0/rf4_server_simulator-v1.2.0-windows-amd64.zip';
const UPDATE_FIXTURE={available:true,channel:'stable',current_version:'v1.1.0',latest_version:'v1.2.0',notes_url:'https://github.com/glance522/rf4_server_simulator/releases/tag/v1.2.0',download_url:'https://github.com/glance522/rf4_server_simulator/releases/latest',download_mirror_url:MIRROR,preview_version:'preview-20261107T110000Z-abc1234',preview_download_mirror:'https://gh-proxy.com/https://github.com/glance522/rf4_server_simulator/releases/download/preview-20261107T110000Z-abc1234/b.zip'};
// 版本更新横幅：/api/me 携带 update 时登录后出现；无 update / available=false 不出现。
test('update banner renders from /api/me payload and hides without update',async()=>{
 const browser=await chromium.launch({headless:true,executablePath:process.env.RF4_TEST_BROWSER||path.join(process.env['ProgramFiles(x86)'],'Microsoft/Edge/Application/msedge.exe')});
 try{
  for(const withUpdate of [true,false]){
   const context=await browser.newContext({viewport:{width:1280,height:900}});
   await context.addInitScript(()=>localStorage.setItem('rf4_admin_token','fixture'));
   const page=await context.newPage(),errors=[];
   page.on('pageerror',e=>errors.push(e.message));
   await page.route('**/*',route=>{
    const url=new URL(route.request().url());
    if(url.pathname==='/')return route.fulfill({contentType:'text/html; charset=utf-8',body:html});
    assert.ok(url.pathname.startsWith('/api/'),url.pathname);
    const key=url.pathname;
    if(key==='/api/me')return route.fulfill({contentType:'application/json',body:JSON.stringify(withUpdate?{username:'admin',update:UPDATE_FIXTURE}:{username:'admin'})});
    if(key==='/api/dashboard')return route.fulfill({contentType:'application/json',body:JSON.stringify(withUpdate?{sessions:1,players:1,items:1,mails:0,tunables:1,audits:0,update:UPDATE_FIXTURE}:{sessions:1,players:1,items:1,mails:0,tunables:1,audits:0})});
    if(key==='/api/audit')return route.fulfill({contentType:'application/json',body:'[]'});
    if(key==='/api/tunables')return route.fulfill({contentType:'application/json',body:'{}'});
    return route.fulfill({contentType:'application/json',body:'{}'});
   });
   await page.goto('http://127.0.0.1:19214/');
   const banner=page.locator('#updateBanner');
   if(withUpdate){
    await banner.waitFor({state:'visible',timeout:3000});
    assert.equal(await page.locator('#updateText').textContent(),'发现新版本 v1.2.0（当前 v1.1.0）');
    assert.equal(await page.locator('#updateMirror').getAttribute('href'),MIRROR,'加速下载应指向 gh-proxy 附件直链');
    assert.equal(await page.locator('#updateDownload').getAttribute('href'),'https://github.com/glance522/rf4_server_simulator/releases/latest');
    assert.equal(await page.locator('#updatePreview').getAttribute('href'),UPDATE_FIXTURE.preview_download_mirror,'最新预览版应指向 preview 加速直链');
    assert.equal(await page.locator('#updatePreview').textContent(),'最新预览版 preview-20261107T110000Z-abc1234');
    assert.equal(await page.locator('#updateDismissBtn').isHidden(),false);
   }else{
    await page.locator('#who').waitFor({timeout:3000});
    assert.equal(await banner.isHidden(),true,'无更新时横幅应隐藏');
   }
   assert.deepEqual(errors,[],withUpdate?'update 渲染不应有页面错误':'无 update 渲染不应有页面错误');
   await context.close();
  }
 }finally{await browser.close()}
});
