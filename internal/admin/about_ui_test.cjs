const {test}=require('node:test');
const assert=require('node:assert/strict'),fs=require('node:fs'),path=require('node:path');
const {chromium}=require('playwright');
const html=fs.readFileSync(path.join(__dirname,'web/index.html'),'utf8');
const README='# 测试项目\n\n这是 **README** 内容。\n\n| 项目 | 地址 |\n|---|---|\n| 部署 | [文档](docs/deploy.md) |\n\n\x60\x60\x60powershell\nbin\\launcher.exe\n\x60\x60\x60';
test('about page renders embedded README markdown',async()=>{
 const browser=await chromium.launch({headless:true,executablePath:process.env.RF4_TEST_BROWSER});
 try{
  const context=await browser.newContext({viewport:{width:1280,height:900}});
  await context.addInitScript(()=>localStorage.setItem('rf4_admin_token','fixture'));
  const page=await context.newPage(),errors=[];
  page.on('pageerror',e=>errors.push(e.message));
  await page.route('**/*',route=>{
   const url=new URL(route.request().url());
   if(url.pathname==='/')return route.fulfill({contentType:'text/html; charset=utf-8',body:html});
   if(url.pathname==='/api/me')return route.fulfill({contentType:'application/json',body:JSON.stringify({username:'admin'})});
   if(url.pathname==='/api/about')return route.fulfill({contentType:'application/json',body:JSON.stringify({content:README})});
   if(url.pathname==='/api/dashboard')return route.fulfill({contentType:'application/json',body:JSON.stringify({sessions:1,players:1,items:1,mails:0,tunables:1,audits:0})});
   return route.fulfill({contentType:'application/json',body:'{}'});
  });
  await page.goto('http://127.0.0.1:19214/#about');
  await page.locator('#aboutReadme h1').waitFor();
  assert.equal(await page.locator('#aboutReadme h1').textContent(),'测试项目');
  assert.equal(await page.locator('#aboutReadme table').count(),1);
  assert.equal(await page.locator('#aboutReadme a').getAttribute('href'),'https://github.com/glance522/rf4_server_simulator/blob/main/docs/deploy.md');
  assert.equal(await page.locator('#aboutReadme pre code').textContent(),'bin\\launcher.exe');
  assert.equal(await page.locator('#view-about').isVisible(),true);
  assert.deepEqual(errors,[]);
  await context.close();
 }finally{await browser.close()}
});
