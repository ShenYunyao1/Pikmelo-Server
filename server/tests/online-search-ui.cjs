'use strict';

const assert = require('node:assert/strict');
const path = require('node:path');
const {chromium} = require(process.env.MUSIC_SERVER_PLAYWRIGHT || 'playwright');

async function fixture(browser) {
  const page = await browser.newPage({viewport:{width:1100,height:800}});
  await page.setContent('<!doctype html><html><body><main class="content"></main></body></html>');
  await page.addStyleTag({path:path.resolve(__dirname,'../internal/music/online-search.css')});
  await page.evaluate(() => {
    history.replaceState(null,'','#/online/search');
    window.state = {user:{id:'alice',role:'admin'},page:'online'};
    window.$ = id => document.getElementById(id);
    window.isAdministrator = () => state.user?.role === 'admin';
    window.makeButton = (text,action,className='btn') => {
      const button=document.createElement('button');button.type='button';button.textContent=text;button.className=className;button.onclick=action;return button;
    };
    window.calls=[];
    window.source={id:'source-one',name:'<img src=x onerror=alert(1)>',version:'1',enabled:true,sources:{kw:{qualitys:['128k','320k']}}};
    window.item={id:'one',platform:'kw',title:'<script>unsafe</script>',artist:'Artist',album:'Album',duration:120,qualities:['128k'],music_info:{songmid:'one'}};
    window.api=async(path,method='GET',body)=>{
      calls.push({path,method,body});
      if(path==='online/sources')return {sources:[source],worker_available:true};
      if(path.startsWith('online/search?')){
        if(window.delaySearch)return new Promise(resolve=>window.releaseSearch=()=>resolve({items:[item],platforms:[],has_more:false}));
        return {items:[item],platforms:[{platform:'kw',error:''},{platform:'tx',error:'timeout'}],has_more:true};
      }
      if(path.startsWith('browse/playlists?')){
        const query=new URLSearchParams(path.split('?')[1]);
        return {items:[{id:query.get('q')?'playlist-52':'playlist-01',name:query.get('q')||'My playlist',owner_id:'alice'}],offset:0,total:1};
      }
      if(path==='online/jobs'&&method==='POST')return {id:'job'};
      if(path==='online/jobs')return {jobs:[{id:'job',title:'Downloaded',status:'downloading',downloaded:120,total:240,platform:'kw',quality:'128k'}]};
      return {};
    };
  });
  await page.addScriptTag({path:path.resolve(__dirname,'../internal/music/online-search.js')});
  await page.evaluate(async()=>{setupOnlineSearchPage();await loadOnlineSearchPage()});
  return page;
}

(async()=>{
  const browser=await chromium.launch({headless:true,executablePath:process.env.MUSIC_SERVER_BROWSER||undefined});
  try {
    const page=await fixture(browser), errors=[];
    page.on('pageerror',error=>errors.push(error.message));
    assert.equal(await page.locator('#onlineSearchButton').isEnabled(),true);
    await page.fill('#onlineQuery','test');await page.click('#onlineSearchButton');
    await page.locator('.online-result').waitFor();
    assert.equal(await page.locator('.online-result-info .track-title').textContent(),'<script>unsafe</script>');
    assert.equal(await page.locator('#onlineResults script').count(),0);
    assert.match(await page.locator('#onlinePlatformStatus').textContent(),/timeout/);
    await page.getByRole('button',{name:'导入曲库',exact:true}).click();
    await page.waitForFunction(()=>$('onlineImportPlaylist').options.length===2);
    assert.deepEqual(await page.locator('#onlineImportQuality option').evaluateAll(options=>options.map(option=>option.value)),['128k']);
    await page.fill('#onlineTargetSearch','playlist 52');
    await page.waitForFunction(()=>$('onlineImportPlaylist').querySelector('option[value="playlist-52"]'));
    await page.selectOption('#onlineImportPlaylist','playlist-52');
    await page.getByRole('button',{name:'开始导入',exact:true}).click();
    await page.locator('.online-job').waitFor();
    const submitted=await page.evaluate(()=>calls.find(call=>call.path==='online/jobs'&&call.method==='POST').body);
    assert.equal(submitted.target_playlist_id,'playlist-52');assert.equal(submitted.source_id,'source-one');
    assert.equal(await page.locator('.online-job progress').getAttribute('value'),'120');
    await page.evaluate(()=>renderOnlineJobs([{id:'quality-job',title:'Source fallback',platform:'wy',quality:'flac',status:'completed',
      downloaded:3000,total:3000,actual_format:'mp3',actual_bitrate_kbps:320,warning:'音源返回的实际格式为 MP3，与请求的 FLAC 不一致。'}]));
    assert.match(await page.locator('.online-job').textContent(),/请求 flac/);
    assert.match(await page.locator('.online-job').textContent(),/实际音频 MP3 · 320 kbps/);
    assert.match(await page.locator('.online-job').textContent(),/与请求的 FLAC 不一致/);
    await page.evaluate(()=>{setOnlineTab('search');delaySearch=true;searchOnline(1)});
    await page.waitForFunction(()=>typeof releaseSearch==='function');
    await page.evaluate(()=>{clearOnlineSearchWorkspace();state.user={id:'bob',role:'member'};releaseSearch()});
    await page.waitForTimeout(50);
    assert.equal(await page.locator('.online-result').count(),0,'late account response must not repopulate results');
    await page.evaluate(()=>loadOnlineSearchPage());
    assert.equal(await page.locator('[data-online-tab="sources"]').isHidden(),true);
    assert.equal(await page.locator('.online-source').count(),0);
    await page.setViewportSize({width:390,height:844});
    await page.evaluate(()=>{delaySearch=false;$('onlineQuery').value='mobile';return searchOnline(1)});
    assert.ok(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth),'mobile page must fit viewport');
    assert.deepEqual(errors,[]);
    console.log('PASS online search, safe rendering, platform errors, target search, import payload, job progress, account race and member source isolation');
    await page.close();
  } finally {await browser.close()}
})().catch(error=>{console.error(error);process.exitCode=1});
