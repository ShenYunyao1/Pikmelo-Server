'use strict';

const onlineSearchState = {
  owner: null, epoch: 0, generation: 0, searchGeneration: 0, targetGeneration: 0,
  sources: [], workerAvailable: false, tab: 'search', items: [], page: 1,
  hasMore: false, timer: null, polling: false, dialog: null,
};
const onlinePlatformNames = {kw:'酷我', kg:'酷狗', tx:'QQ 音乐', wy:'网易云', mg:'咪咕'};

function onlineElement(tag, className, text) {
  const element = document.createElement(tag);
  if (className) element.className = className;
  if (text !== undefined) element.textContent = String(text);
  return element;
}
function onlineField(label, input) {
  const field = onlineElement('label', 'field', label);
  field.append(input);
  return field;
}
function onlineSelect(options) {
  const select = document.createElement('select');
  for (const [value, label] of options) {
    const option = onlineElement('option', '', label);
    option.value = value;
    select.append(option);
  }
  return select;
}
function clearOnlineSearchWorkspace() {
  onlineSearchState.epoch++;
  onlineSearchState.generation++;
  onlineSearchState.searchGeneration++;
  onlineSearchState.targetGeneration++;
  clearTimeout(onlineSearchState.timer);
  onlineSearchState.timer = null;
  onlineSearchState.polling = false;
  onlineSearchState.dialog?.close();
  onlineSearchState.owner = null;
  onlineSearchState.sources = [];
  onlineSearchState.items = [];
  onlineSearchState.tab = 'search';
  onlineSearchState.page = 1;
  onlineSearchState.hasMore = false;
  for (const id of ['onlineResults', 'onlineJobs', 'onlineSources', 'onlinePlatformStatus', 'onlineNotice']) $(id)?.replaceChildren();
  if ($('onlineQuery')) $('onlineQuery').value = '';
}
function onlineCurrent(generation, owner = onlineSearchState.owner, epoch = onlineSearchState.epoch) {
  return Boolean(owner) && state.user?.id === owner && onlineSearchState.owner === owner &&
    onlineSearchState.epoch === epoch && onlineSearchState.generation === generation &&
    state.page === 'online' && location.hash.split('?')[0] === '#/online/search';
}
function onlinePageChanged() {
  onlineSearchState.generation++;
  onlineSearchState.searchGeneration++;
  clearTimeout(onlineSearchState.timer);
  onlineSearchState.timer = null;
  onlineSearchState.dialog?.close();
}
function setupOnlineSearchPage() {
  const page = onlineElement('section', 'page');
  page.id = 'page-online';
  const intro = onlineElement('div', 'page-intro');
  intro.append(onlineElement('h1', '', '在线搜索'));
  const tabs = onlineElement('nav', 'page-tabs');
  tabs.setAttribute('aria-label', '在线搜索功能');
  for (const [tab, label] of [['search','搜索'], ['jobs','下载任务'], ['sources','音源管理']]) {
    const button = makeButton(label, () => setOnlineTab(tab));
    button.dataset.onlineTab = tab;
    tabs.append(button);
  }
  const notice = onlineElement('p', 'notice');
  notice.id = 'onlineNotice'; notice.setAttribute('role', 'status');
  const search = onlineElement('section', 'panel online-search-panel');
  search.id = 'onlineSearchPanel';
  const form = onlineElement('form', 'online-search-controls');
  const query = onlineElement('input');
  query.id = 'onlineQuery'; query.type = 'search'; query.maxLength = 200;
  query.placeholder = '输入曲名、歌手或专辑';
  const platform = onlineSelect([['all','全部可用平台']]); platform.id = 'onlinePlatform';
  const size = onlineSelect([['20','20 首'], ['30','30 首'], ['50','50 首']]); size.id = 'onlinePageSize';
  const submit = makeButton('搜索', () => {} , 'btn primary'); submit.id = 'onlineSearchButton'; submit.type = 'submit';
  form.append(onlineField('关键词',query),onlineField('平台',platform),onlineField('各平台每页数量',size),submit);
  form.onsubmit = event => {event.preventDefault();searchOnline(1)};
  platform.onchange = size.onchange = () => {if (query.value.trim()) searchOnline(1)};
  const platforms = onlineElement('div', 'online-platform-status'); platforms.id = 'onlinePlatformStatus'; platforms.setAttribute('role','status');
  const results = onlineElement('div', 'online-results'); results.id = 'onlineResults'; results.setAttribute('role','list');
  const pager = onlineElement('nav', 'online-pager'); pager.id = 'onlinePager'; pager.setAttribute('aria-label','在线搜索分页');
  search.append(form, platforms, results, pager);
  const jobs = onlineElement('section','panel'); jobs.id = 'onlineJobsPanel'; jobs.hidden = true;
  jobs.append(makeButton('刷新任务',()=>loadOnlineJobs()),onlineElement('p','status','下载在服务器继续运行，关闭网页后可以回来查看。'));
  const jobList = onlineElement('div','online-jobs'); jobList.id = 'onlineJobs'; jobs.append(jobList);
  const sources = onlineElement('section','panel'); sources.id = 'onlineSourcesPanel'; sources.hidden = true;
  sources.append(makeButton('导入音源链接',()=>openOnlineSourceImport(),'btn primary'),onlineElement('p','status','导入单个 LX JavaScript 音源链接，测试初始化后再启用。'));
  const sourceList = onlineElement('div','online-sources'); sourceList.id = 'onlineSources'; sources.append(sourceList);
  page.append(intro,tabs,notice,search,jobs,sources);
  document.querySelector('.content').append(page);
  document.addEventListener('visibilitychange',()=>{
    clearTimeout(onlineSearchState.timer);
    if (!document.hidden && state.page === 'online' && onlineSearchState.tab === 'jobs') loadOnlineJobs();
  });
}
async function loadOnlineSearchPage() {
  if (onlineSearchState.owner !== state.user?.id) clearOnlineSearchWorkspace();
  onlineSearchState.owner = state.user?.id;
  const generation = onlineSearchState.generation;
  $('onlineNotice').textContent = '正在检查音源…';
  try {
    const result = await api('online/sources');
    if (!onlineCurrent(generation)) return;
    onlineSearchState.sources = Array.isArray(result.sources) ? result.sources : [];
    onlineSearchState.workerAvailable = Boolean(result.worker_available);
    updateOnlineSourceControls();
    setOnlineTab(onlineSearchState.tab);
  } catch (error) {if (onlineCurrent(generation)) $('onlineNotice').textContent = error.message}
}
function updateOnlineSourceControls() {
  const available = new Set();
  for (const source of onlineSearchState.sources) {
    if (source.enabled) for (const platform of Object.keys(source.sources || {})) if (onlinePlatformNames[platform]) available.add(platform);
  }
  const select = $('onlinePlatform'), previous = select.value;
  select.replaceChildren(...onlineSelect([['all','全部可用平台'], ...Object.keys(onlinePlatformNames).filter(key=>available.has(key)).map(key=>[key,onlinePlatformNames[key]])]).children);
  if ([...select.options].some(option=>option.value===previous)) select.value = previous;
  const usable = onlineSearchState.workerAvailable && available.size > 0;
  $('onlineSearchButton').disabled = !usable;
  $('onlineNotice').textContent = !onlineSearchState.workerAvailable ? '在线音源 worker 尚未就绪，请管理员检查 Docker 配置。' : !available.size ? '尚无已启用的可搜索音源，请管理员导入并启用音源。' : '选择平台并搜索，导入后曲目会加入服务器曲库。';
  document.querySelector('[data-online-tab="sources"]').hidden = !isAdministrator();
  renderOnlineSources();
}
function setOnlineTab(tab) {
  if (tab === 'sources' && !isAdministrator()) tab = 'search';
  onlineSearchState.tab = tab;
  clearTimeout(onlineSearchState.timer);
  for (const [key,id] of [['search','onlineSearchPanel'],['jobs','onlineJobsPanel'],['sources','onlineSourcesPanel']]) $(id).hidden = tab !== key;
  for (const button of document.querySelectorAll('[data-online-tab]')) button.classList.toggle('active',button.dataset.onlineTab===tab);
  if (tab === 'jobs') loadOnlineJobs();
}
async function searchOnline(page) {
  const query = $('onlineQuery').value.trim();
  if (!query) { $('onlinePlatformStatus').textContent = '请输入关键词。'; return }
  const generation = onlineSearchState.generation, searchGeneration = ++onlineSearchState.searchGeneration;
  const current = () => onlineCurrent(generation) && searchGeneration === onlineSearchState.searchGeneration;
  const params = new URLSearchParams({q:query,platform:$('onlinePlatform').value,page:String(page),limit:$('onlinePageSize').value});
  $('onlineSearchButton').disabled = true;
  $('onlinePlatformStatus').textContent = '正在搜索…';
  try {
    const result = await api('online/search?'+params);
    if (!current()) return;
    onlineSearchState.items = Array.isArray(result.items) ? result.items : [];
    onlineSearchState.page = page;
    onlineSearchState.hasMore = Boolean(result.has_more);
    const messages = (result.platforms || []).map(item=>(onlinePlatformNames[item.platform] || item.platform)+(item.error?' · '+item.error:' · 已返回'));
    $('onlinePlatformStatus').textContent = messages.join('，');
    renderOnlineResults();
  } catch (error) {if (current()) $('onlinePlatformStatus').textContent = '搜索失败，已保留当前结果。'+error.message}
  finally {if (current()) $('onlineSearchButton').disabled = false}
}
function renderOnlineResults() {
  const host = $('onlineResults'); host.replaceChildren();
  if (!onlineSearchState.items.length) host.append(onlineElement('p','empty','没有匹配的曲目。'));
  for (const item of onlineSearchState.items) {
    const row = onlineElement('article','online-result'); row.setAttribute('role','listitem');
    const cover = onlineElement('span','online-cover','♪'); cover.setAttribute('aria-hidden','true');
    const info = onlineElement('div','online-result-info');
    const title = makeButton(item.title || '未命名曲目',()=>openOnlineDetails(item),'text-button track-title');
    info.append(title,onlineElement('span','status',[item.artist || '未知歌手',item.album].filter(Boolean).join(' · ')));
    const source = onlineElement('span','badge',onlinePlatformNames[item.platform] || item.platform);
    const duration = onlineElement('span','status',onlineDuration(item.duration));
    const add = makeButton('导入曲库',()=>openOnlineImport(item),'btn small');
    row.append(cover,info,source,duration,add);host.append(row);
  }
  const pager = $('onlinePager'); pager.replaceChildren();
  const previous = makeButton('上一页',()=>searchOnline(onlineSearchState.page-1));previous.disabled = onlineSearchState.page <= 1;
  const next = makeButton('下一页',()=>searchOnline(onlineSearchState.page+1));next.disabled = !onlineSearchState.hasMore;
  pager.append(previous,onlineElement('span','status','第 '+onlineSearchState.page+' 页 · 本页 '+onlineSearchState.items.length+' 首'),next);
}
function onlineDuration(seconds) {
  const value = Math.max(0,Math.floor(Number(seconds)||0));
  return Math.floor(value/60)+':'+String(value%60).padStart(2,'0');
}
function onlineDialog(title) {
  onlineSearchState.dialog?.close();
  const dialog = document.createElement('dialog'); dialog.className = 'online-dialog';
  const heading = onlineElement('h2','',title), body = onlineElement('div','online-dialog-body'), footer = onlineElement('div','actions');
  const close = makeButton('关闭',()=>dialog.close());footer.append(close);
  dialog.append(heading,body,footer);document.body.append(dialog);
  dialog.addEventListener('click',event=>{if(event.target===dialog){const rect=dialog.getBoundingClientRect();if(event.clientX<rect.left||event.clientX>rect.right||event.clientY<rect.top||event.clientY>rect.bottom)dialog.close()}});
  dialog.addEventListener('close',()=>{onlineSearchState.targetGeneration++;dialog.remove();if(onlineSearchState.dialog===dialog)onlineSearchState.dialog=null},{once:true});
  onlineSearchState.dialog = dialog;dialog.showModal();
  return {dialog,body,footer};
}
function openOnlineDetails(item) {
  const {body} = onlineDialog('曲目详情');
  for (const [label,value] of [['曲名',item.title],['歌手',item.artist],['专辑',item.album],['平台',onlinePlatformNames[item.platform] || item.platform],['时长',onlineDuration(item.duration)]]) body.append(onlineElement('p','',label+' · '+(value || '—')));
}
function openOnlineImport(item) {
  const {dialog,body,footer} = onlineDialog('导入曲库');
  body.append(onlineElement('p','',item.title || '未命名曲目'));
  const candidates = onlineSearchState.sources.filter(source=>source.enabled && source.sources?.[item.platform]);
  const source = onlineSelect(candidates.map(value=>[value.id,value.name])); source.id = 'onlineImportSource';
  const quality = document.createElement('select'); quality.id = 'onlineImportQuality';
  const updateQualities = () => {
    const selected = candidates.find(value=>value.id===source.value);
    const choices = selected?.sources?.[item.platform]?.qualitys || [];
    const usable = item.qualities?.length ? choices.filter(value=>item.qualities.includes(value)) : choices;
    quality.replaceChildren(...onlineSelect(usable.map(value=>[value,value])).children);
    submit.disabled = !quality.options.length;
  };
  const search = onlineElement('input'); search.type = 'search';search.placeholder = '搜索我的歌单';search.id = 'onlineTargetSearch';
  const target = onlineSelect([['','仅导入全局曲库']]);target.id = 'onlineImportPlaylist';
  const targetPager = onlineElement('div','online-pager');
  const status = onlineElement('p','status');status.setAttribute('role','status');
  let selectedTarget = '', targetQuery = '';
  target.onchange = () => {selectedTarget=target.value};
  const generation = onlineSearchState.generation;
  const current = () => onlineCurrent(generation) && dialog.isConnected && dialog.open;
  const loadTargets = async(offset=0) => {
    const request = ++onlineSearchState.targetGeneration, query = search.value.trim();
    try {
      const page = await api('browse/playlists?'+new URLSearchParams({q:query,offset:String(offset),limit:'30',summary_only:'1'}));
      if (!current() || request!==onlineSearchState.targetGeneration) return;
      const items = (page.items || []).filter(value=>!value.system&&!value.read_only&&value.owner_id===state.user.id);
      const options = [['','仅导入全局曲库'],...items.map(value=>[value.id,value.name])];
      const previousOption = [...target.options].find(value=>value.value===selectedTarget);
      if (selectedTarget && previousOption && !items.some(value=>value.id===selectedTarget)) options.push([selectedTarget,previousOption.textContent]);
      target.replaceChildren(...onlineSelect(options).children);target.value = selectedTarget;
      targetPager.replaceChildren();
      const previous=makeButton('上一页',()=>loadTargets(Math.max(0,offset-30)),'btn small');previous.disabled=offset===0;
      const next=makeButton('下一页',()=>loadTargets(offset+30),'btn small');next.disabled=offset+(page.items||[]).length>=page.total;
      targetPager.append(previous,onlineElement('span','status','第 '+(Math.floor(offset/30)+1)+' 页'),next);
      targetQuery=query;
    } catch(error) {if(current() && request===onlineSearchState.targetGeneration)status.textContent='读取目标歌单失败。'+error.message}
  };
  search.oninput = () => {if(search.value.trim()!==targetQuery)loadTargets()};
  const submit = makeButton('开始导入',async()=>{
    submit.disabled=true;status.textContent='正在创建任务…';
    try {
      await api('online/jobs','POST',{source_id:source.value,item,quality:quality.value,target_playlist_id:target.value});
      if(!current())return;
      dialog.close();setOnlineTab('jobs');
    } catch(error){if(current()){status.textContent=error.message;submit.disabled=false}}
  },'btn primary');
  source.onchange=updateQualities;
  body.append(onlineField('音源',source),onlineField('音质',quality),onlineField('目标歌单搜索',search),onlineField('目标歌单',target),targetPager,status);
  footer.prepend(submit);updateQualities();loadTargets();
}
function onlineBytes(bytes) {
  const value=Math.max(0,Number(bytes)||0);
  return value>=1048576?(value/1048576).toFixed(1)+' MiB':(value/1024).toFixed(0)+' KiB';
}
async function loadOnlineJobs() {
  clearTimeout(onlineSearchState.timer);
  const generation=onlineSearchState.generation;
  if(!onlineCurrent(generation)||onlineSearchState.tab!=='jobs'||document.hidden||onlineSearchState.polling===generation)return;
  onlineSearchState.polling=generation;
  try {
    const result=await api('online/jobs');
    if(!onlineCurrent(generation)||onlineSearchState.tab!=='jobs')return;
    renderOnlineJobs(Array.isArray(result.jobs)?result.jobs:[]);
  } catch(error){if(onlineCurrent(generation))$('onlineJobs').textContent='读取失败。'+error.message}
  finally {
    if(onlineSearchState.polling===generation)onlineSearchState.polling=false;
    if(onlineCurrent(generation)&&onlineSearchState.tab==='jobs'&&!document.hidden)onlineSearchState.timer=setTimeout(loadOnlineJobs,5000);
  }
}
function renderOnlineJobs(jobs) {
  const host=$('onlineJobs');host.replaceChildren();
  if(!jobs.length)host.append(onlineElement('p','empty','暂无下载任务。'));
  const labels={queued:'排队中',resolving:'解析地址',downloading:'下载中',validating:'校验与入库',completed:'已完成',failed:'失败',cancelled:'已取消'};
  for(const job of jobs){
    const row=onlineElement('article','online-job'),info=onlineElement('div','online-result-info');
    info.append(onlineElement('strong','',job.title||'未命名曲目'),onlineElement('span','status',[onlinePlatformNames[job.platform]||job.platform,job.quality?'请求 '+job.quality:'',labels[job.status]||job.status].filter(Boolean).join(' · ')));
    if(job.actual_format){
      const actual=[String(job.actual_format).toUpperCase(),job.actual_bitrate_kbps>0?job.actual_bitrate_kbps+' kbps':''].filter(Boolean).join(' · ');
      info.append(onlineElement('span','status','实际音频 '+actual));
    }
    const progress=document.createElement('progress');progress.max=Number(job.total)||1;
    if(job.total>0)progress.value=Math.min(Number(job.downloaded)||0,progress.max);
    progress.setAttribute('aria-label','下载进度');
    const amount=onlineElement('span','status',onlineBytes(job.downloaded)+(job.total>0?' / '+onlineBytes(job.total):''));
    info.append(progress,amount);
    if(job.error)info.append(onlineElement('p','status error',job.error));
    if(job.warning)info.append(onlineElement('p','status',job.warning));
    const actions=onlineElement('div','actions');
    const operation=['failed','cancelled'].includes(job.status)?'retry':!['completed'].includes(job.status)?'cancel':null;
    if(operation){
      const button=makeButton(operation==='retry'?'重试':'取消',async()=>{
        const generation=onlineSearchState.generation;button.disabled=true;
        try {await api('online/jobs/'+encodeURIComponent(job.id)+'/'+operation,'POST');if(onlineCurrent(generation))loadOnlineJobs()}
        catch(error){if(onlineCurrent(generation)){button.disabled=false;info.append(onlineElement('p','status error',error.message))}}
      },'btn small');actions.append(button);
    }
    if(job.track_id&&job.status==='completed')actions.append(makeButton('在曲库查看',()=>{location.hash='#/library?q='+encodeURIComponent(job.title||'')},'btn small'));
    row.append(info,actions);host.append(row);
  }
}
function renderOnlineSources() {
  const host=$('onlineSources');host.replaceChildren();
  if(!isAdministrator())return;
  if(!onlineSearchState.sources.length)host.append(onlineElement('p','empty','尚未导入音源。'));
  for(const source of onlineSearchState.sources){
    const row=onlineElement('article','online-source'),info=onlineElement('div','online-result-info');
    info.append(onlineElement('strong','',source.name||'未命名音源'),onlineElement('span','status',[source.version,source.enabled?'已启用':'未启用',Object.keys(source.sources||{}).map(value=>onlinePlatformNames[value]||value).join('、')].filter(Boolean).join(' · ')));
    const actions=onlineElement('div','actions');
    const change=makeButton(source.enabled?'停用':'启用',()=>mutateOnlineSource(source,'PATCH',{enabled:!source.enabled}),'btn small');
    const remove=makeButton('删除',()=>{if(confirm('删除音源“'+(source.name||'未命名音源')+'”？'))mutateOnlineSource(source,'DELETE')},'btn small danger');
    actions.append(change,remove);row.append(info,actions);host.append(row);
  }
}
async function mutateOnlineSource(source,method,payload) {
  const generation=onlineSearchState.generation;
  try{await api('online/sources/'+encodeURIComponent(source.id),method,payload);if(onlineCurrent(generation))await loadOnlineSearchPage()}
  catch(error){if(onlineCurrent(generation))$('onlineNotice').textContent=error.message}
}
function openOnlineSourceImport() {
  if(!isAdministrator())return;
  const {dialog,body,footer}=onlineDialog('导入 LX 音源');
  const input=onlineElement('input');input.type='url';input.autocomplete='off';input.placeholder='https://example.com/source.js';input.id='onlineSourceURL';
  const status=onlineElement('p','status');status.setAttribute('role','status');
  const generation=onlineSearchState.generation,current=()=>onlineCurrent(generation)&&dialog.isConnected&&dialog.open;
  const submit=makeButton('导入并测试',async()=>{
    let url;
    try{url=new URL(input.value.trim());if(!['http:','https:'].includes(url.protocol))throw new Error()}
    catch(_){status.textContent='请输入完整的 HTTP 或 HTTPS 音源链接。';return}
    submit.disabled=true;status.textContent='正在下载脚本并测试初始化…';
    try{await api('online/sources','POST',{url:url.href});if(!current())return;input.value='';dialog.close();await loadOnlineSearchPage()}
    catch(error){if(current()){status.textContent=error.message;submit.disabled=false}}
  },'btn primary');
  body.append(onlineField('音源链接',input),status);footer.prepend(submit);
  dialog.addEventListener('close',()=>{input.value=''},{once:true});input.focus();
}
