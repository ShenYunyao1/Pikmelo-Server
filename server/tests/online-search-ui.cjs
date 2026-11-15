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
    window.item={id:'one',platform:'kw',title:'<script>unsafe</script>',artist:'Artist',album:'Album',duration:120,qualities:['128k'],
      quality_options:[{quality:'128k',format:'MP3',bitrate_kbps:128,size_bytes:1048576,sample_rate:44100}],music_info:{songmid:'one'}};
    window.api=async(path,method='GET',body)=>{
      calls.push({path,method,body});
      if(path==='online/sources')return {sources:window.allSources||[source],worker_available:true};
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
    const replacement = await page.evaluate(() => {
      openOnlineDetails(item);
      const previous = onlineSearchState.dialog;
      previous.close();
      openOnlineImport(item, '128k');
      const generation = onlineSearchState.targetGeneration;
      const count = document.querySelectorAll('dialog.online-dialog').length;
      previous.dispatchEvent(new Event('close'));
      return {count, generationRetained: generation === onlineSearchState.targetGeneration};
    });
    assert.equal(replacement.count, 1, 'Replacing a dialog must immediately detach the previous dialog');
    assert.equal(replacement.generationRetained, true, 'A delayed close event must preserve the replacement dialog request');
    await page.waitForFunction(()=>$('onlineImportPlaylist').options.length===2);
    await page.locator('dialog').getByRole('button',{name:'关闭',exact:true}).click();
    assert.equal(await page.locator('#onlineSearchButton').isEnabled(),true);
    await page.fill('#onlineQuery','test');await page.click('#onlineSearchButton');
    await page.locator('.online-result').waitFor();
    assert.equal(await page.locator('.online-result-info .track-title').textContent(),'<script>unsafe</script>');
    assert.equal(await page.locator('#onlineResults script').count(),0);
    assert.match(await page.locator('#onlinePlatformStatus').textContent(),/timeout/);
    assert.match(await page.locator('.online-quality-chip').first().textContent(),/MP3/);
    assert.match(await page.locator('.online-quality-chip').first().textContent(),/1\.0 MiB/);
    await page.locator('.online-quality-chip').first().click();
    assert.equal(await page.inputValue('#onlineImportQuality'),'128k');
    assert.match(await page.locator('#onlineQualityDetails').textContent(),/44\.1 kHz/);
    assert.equal(await page.evaluate(()=>calls.filter(call=>call.path==='online/jobs'&&call.method==='POST').length),0,'Selecting quality opens settings before a download is submitted');
    await page.locator('dialog').getByRole('button',{name:'关闭',exact:true}).click();
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
    await page.evaluate(()=>{
      source.sources.kw.qualitys=['128k'];
      allSources=[source,{id:'lossless',name:'Lossless source',enabled:true,sources:{kw:{qualitys:['flac']}}}];
      onlineSearchState.sources=allSources;
      item.qualities=['128k','320k','flac'];
      item.quality_options=[
        {quality:'128k',format:'MP3',size_bytes:1048576},
        {quality:'320k',format:'MP3',bitrate_kbps:320,size_bytes:3145728},
        {quality:'flac',format:'FLAC',size_bytes:8388608,sample_rate:96000,bit_depth:24,channels:2},
        {quality:'96aac',label:'AAC',codec:'AAC',bitrate_kbps:96,size_bytes:800000},
        {quality:'extra',label:'<img src=x onerror=alert(1)>',format:'<script>unsafe</script>'},
      ];
      setOnlineTab('search');onlineSearchState.items=[item];renderOnlineResults();
    });
    assert.equal(await page.locator('.online-quality-chip[disabled]').count(),0,'Unsupported candidates are read-only text');
    assert.equal(await page.locator('.online-quality-chip[data-quality="320k"]').evaluate(el=>el.tagName),'SPAN');
    await page.locator('button.online-quality-chip[data-quality="flac"]').focus();
    await page.keyboard.press('Enter');
    assert.equal(await page.inputValue('#onlineImportSource'),'lossless');
    assert.equal(await page.inputValue('#onlineImportQuality'),'flac');
    assert.match(await page.locator('#onlineQualityDetails').textContent(),/96 kHz/);
    assert.match(await page.locator('#onlineQualityDetails').textContent(),/24-bit/);
    await page.selectOption('#onlineImportSource','source-one');
    assert.equal(await page.inputValue('#onlineImportQuality'),'128k');
    assert.doesNotMatch(await page.locator('#onlineQualityDetails').textContent(),/96 kHz/);
    await page.locator('dialog').getByRole('button',{name:'关闭',exact:true}).click();
    await page.getByRole('button',{name:'更多音质',exact:false}).click();
    assert.equal(await page.locator('.online-quality-table tbody tr').count(),5);
    assert.ok((await page.locator('dialog').boundingBox()).width>700,'Desktop quality details should have room for attribute columns');
    assert.match(await page.locator('.online-quality-table').textContent(),/当前音源未支持/);
    assert.equal(await page.locator('.online-quality-table img,.online-quality-table script').count(),0);
    await page.locator('dialog').getByRole('button',{name:'关闭',exact:true}).click();
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
    await page.locator('button.online-quality-chip[data-quality="flac"]').click();
    assert.ok(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth),'mobile import metadata must fit viewport');
    await page.locator('dialog').getByRole('button',{name:'关闭',exact:true}).click();
    await page.getByRole('button',{name:'更多音质',exact:false}).click();
    assert.ok(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth),'quality table must scroll inside the dialog');
    await page.locator('dialog').getByRole('button',{name:'关闭',exact:true}).click();
    await page.evaluate(()=>{item.quality_options=[{quality:'128k',bitrate_kbps:-1,size_bytes:0,sample_rate:NaN}];item.qualities=['128k'];onlineSearchState.items=[item];renderOnlineResults();openOnlineDetails(item)});
    assert.deepEqual(await page.locator('.online-quality-table thead th').allTextContents(),['音质','可用音源']);
    await page.locator('dialog').getByRole('button',{name:'关闭',exact:true}).click();
    await page.evaluate(()=>{delete item.quality_options;renderOnlineResults()});
    assert.equal(await page.locator('.online-quality-chip').first().textContent(),'128k','Earlier candidates must retain tier selection');
    await page.evaluate(()=>{item.quality_options=[{quality:'128k',format:'MP3',size_bytes:1000},{quality:'128k',format:'MP3',size_bytes:2000}];openOnlineDetails(item)});
    assert.equal(await page.locator('.online-quality-table tbody tr').count(),2,'Distinct reported variants must remain visible');
    await page.locator('dialog').getByRole('button',{name:'关闭',exact:true}).click();
    await page.evaluate(()=>{onlineSearchState.sources=[{id:'unmatched',name:'Other tier',enabled:true,sources:{kw:{qualitys:['320k']}}}];openOnlineImport(item)});
    assert.equal(await page.getByRole('button',{name:'开始导入',exact:true}).isDisabled(),true);
    assert.match(await page.locator('dialog').textContent(),/没有交集/);
    assert.deepEqual(errors,[]);
    console.log('PASS online search, platform quality metadata, preselected source/tier, keyboard, missing/legacy fields, mobile dialogs, XSS, target search and account isolation');
    await page.close();
  } finally {await browser.close()}
})().catch(error=>{console.error(error);process.exitCode=1});
