const {test}=require('node:test');
const assert=require('node:assert/strict'),fs=require('node:fs'),path=require('node:path');
const {chromium}=require('playwright');
const html=fs.readFileSync(path.join(__dirname,'web/index.html'),'utf8');
const NOTE='# 发布说明\n\n- 修复 **登录**\n- 来自发布包内的 发布说明.md';
test('release notes page renders local package markdown',async()=>{
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
   if(url.pathname==='/api/release-notes')return route.fulfill({contentType:'application/json',body:JSON.stringify({filename:'发布说明.md',content:NOTE})});
   if(url.pathname==='/api/dashboard')return route.fulfill({contentType:'application/json',body:JSON.stringify({sessions:1,players:1,items:1,mails:0,tunables:1,audits:0})});
   return route.fulfill({contentType:'application/json',body:'{}'});
  });
  await page.goto('http://127.0.0.1:19214/#release');
  await page.locator('#releaseBody h1').waitFor();
  assert.equal(await page.locator('#releaseBody h1').textContent(),'发布说明');
  assert.equal(await page.locator('#releaseBody strong').textContent(),'登录');
  assert.equal(await page.locator('#releaseBody li').count(),2);
  assert.equal(await page.locator('#view-release').isVisible(),true);
  assert.deepEqual(errors,[]);
  await context.close();
 }finally{await browser.close()}
});
