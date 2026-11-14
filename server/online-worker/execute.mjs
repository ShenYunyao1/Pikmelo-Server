import vm from 'node:vm';
import { workerData, parentPort } from 'node:worker_threads';
import { createHash, createCipheriv, randomBytes, publicEncrypt, constants, createPublicKey } from 'node:crypto';
import { inflate, deflate } from 'node:zlib';

const maxBytes = 4 * 1024 * 1024;
const platforms = new Set(['kw','kg','tx','wy','mg','local']);
const qualities = new Set(['128k','320k','flac','flac24bit']);
const EVENTS = Object.freeze({inited:'inited',request:'request',updateAlert:'updateAlert'});
// A source may await a Promise without active timers. The parent owns the deadline.
const lifetime = setInterval(() => {}, 1000);

function normalizeSources(data) {
  if (!data || typeof data !== 'object' || Array.isArray(data)) throw new Error('Invalid source declarations');
  const result = {};
  for (const [platform, item] of Object.entries(data)) {
    if (!platforms.has(platform) || !item || item.type !== 'music' || !Array.isArray(item.actions) || !Array.isArray(item.qualitys)) {
      throw new Error('Invalid platform declaration');
    }
    const actions = platform === 'local' ? ['musicUrl','lyric','pic'] : ['musicUrl'];
    if (!item.actions.length || item.actions.some(action=>!actions.includes(action)) || item.qualitys.some(quality=>!qualities.has(quality))) {
      throw new Error('Invalid actions or qualities');
    }
    if (platform !== 'local' && !item.qualitys.length) throw new Error('Source quality list is empty');
    result[platform] = {name:String(item.name || platform).slice(0,128),type:'music',actions:[...new Set(item.actions)],qualitys:[...new Set(item.qualitys)]};
  }
  if (!Object.keys(result).length) throw new Error('Source does not declare a platform');
  return result;
}

function metadata(script) {
  const header = script.match(/^\s*\/\*\*([\s\S]*?)\*\//)?.[1] || '';
  const result = {name:'Imported source',description:'',version:'',author:'',homepage:'',rawScript:script};
  for (const field of ['name','description','version','author','homepage']) {
    const value = header.match(new RegExp('@'+field+'[ \\t]+([^\\r\\n*]+)'))?.[1].trim();
    if (value) result[field]=value.slice(0,1024);
  }
  return Object.freeze(result);
}

function checkedBuffer(value, encoding) {
  if (typeof value === 'string' && value.length > maxBytes) throw new Error('Buffer exceeds limit');
  if (value?.length > maxBytes) throw new Error('Buffer exceeds limit');
  const result = Buffer.from(value, encoding);
  if (result.length > maxBytes) throw new Error('Buffer exceeds limit');
  return result;
}

function compression(fn, value) {
  const input = checkedBuffer(value);
  return new Promise((resolve,reject)=>fn(input,{maxOutputLength:maxBytes},(error,result)=>error?reject(new Error('Compression failed')):resolve(result)));
}

function brokerRequest(url, options, callback) {
  if (typeof options === 'function') { callback=options; options={}; }
  options ||= {};
  if (typeof callback !== 'function') throw new Error('HTTP callback required');
  const controller = new AbortController();
  const timer = setTimeout(()=>controller.abort(),Math.min(14000,Math.max(100,Number(options.timeout)||14000)));
  let cancelled=false;
  const broker = workerData.broker;
  Promise.resolve().then(async()=>{
    if (!broker || typeof broker.url !== 'string' || !broker.token) throw new Error('HTTP broker unavailable');
    let body=options.body;
    const headers = {...options.headers};
    if (options.formData) throw new Error('Multipart requests are unsupported');
    if (options.form) {
      body = new URLSearchParams(options.form).toString();
      headers['Content-Type']='application/x-www-form-urlencoded';
    }
    const payload = JSON.stringify({url,method:String(options.method || 'GET').toUpperCase(),headers,body});
    if (Buffer.byteLength(payload) > maxBytes) throw new Error('HTTP request exceeds limit');
    const response = await fetch(broker.url,{
      method:'POST',headers:{'Content-Type':'application/json',Authorization:'Bearer '+broker.token},
      body:payload,signal:controller.signal,redirect:'error',
    });
    let length=0;
    const chunks=[];
    for await (const chunk of response.body) {
      length+=chunk.length;
      if (length>maxBytes) { controller.abort(); throw new Error('HTTP response exceeds limit'); }
      chunks.push(chunk);
    }
    if (!response.ok) throw new Error('HTTP broker rejected request');
    const reply=JSON.parse(Buffer.concat(chunks).toString('utf8'));
    if (!Number.isInteger(reply.statusCode)) throw new Error('Invalid broker response');
    if (typeof reply.body === 'string') {
      const contentType=Object.entries(reply.headers || {}).find(([key])=>key.toLowerCase()==='content-type')?.[1];
      if (String(contentType).includes('json')) {
        try { reply.body=JSON.parse(reply.body); } catch {}
      }
    }
    if (!cancelled) callback(null,reply,reply.body);
  }).catch(error=>{ if (!cancelled) callback(new Error(error.message),null,null); }).finally(()=>clearTimeout(timer));
  return ()=>{cancelled=true;clearTimeout(timer);controller.abort();};
}

async function execute() {
  let handler;
  let initialized=false;
  let initialize;
  const ready = new Promise(resolve=>{initialize=resolve;});
  const lx = {
    version:'2.0.0',env:'desktop',currentScriptInfo:metadata(workerData.script),EVENT_NAMES:EVENTS,
    on(event, callback) {
      if (event!==EVENTS.request || typeof callback!=='function') throw new Error('Unsupported event handler');
      handler=callback;
      return Promise.resolve();
    },
    send(event, data) {
      if (event===EVENTS.updateAlert) return Promise.resolve();
      if (event!==EVENTS.inited || initialized) throw new Error('Invalid initialization event');
      const sources=normalizeSources(data?.sources);
      initialized=true;
      initialize(sources);
      return Promise.resolve();
    },
    request:brokerRequest,
    utils:{
      buffer:{from:checkedBuffer,bufToString:(value,format)=>checkedBuffer(value).toString(format)},
      crypto:{
        md5:value=>createHash('md5').update(checkedBuffer(value)).digest('hex'),
        randomBytes(size) { if (!Number.isInteger(size)||size<0||size>maxBytes) throw new Error('Invalid random buffer size'); return randomBytes(size); },
        aesEncrypt(value, mode, key, iv) {
          const cipher=createCipheriv(mode,checkedBuffer(key),iv===null?null:checkedBuffer(iv));
          return Buffer.concat([cipher.update(checkedBuffer(value)),cipher.final()]);
        },
        rsaEncrypt(value,key) {
          const input=checkedBuffer(value);
          const publicKey=createPublicKey(key);
          const keyBytes=Math.ceil(publicKey.asymmetricKeyDetails.modulusLength/8);
          if (keyBytes>16384 || input.length>keyBytes) throw new Error('Invalid RSA input size');
          return publicEncrypt({key:publicKey,padding:constants.RSA_NO_PADDING},Buffer.concat([Buffer.alloc(keyBytes-input.length),input]));
        },
      },
      zlib:{inflate:value=>compression(inflate,value),deflate:value=>compression(deflate,value)},
    },
  };
  const context=vm.createContext({
    lx,console:Object.freeze({log(){},warn(){},error(){},info(){},debug(){}}),
    setTimeout,clearTimeout,setInterval,clearInterval,queueMicrotask,
    TextEncoder,TextDecoder,URL,URLSearchParams,
    atob:value=>checkedBuffer(value,'base64').toString('binary'),
    btoa:value=>checkedBuffer(value,'binary').toString('base64'),
  },{codeGeneration:{strings:false,wasm:false}});
  // The container and broker are the isolation boundary. vm only provides API scoping.
  new vm.Script(workerData.script,{filename:'source.js'}).runInContext(context,{timeout:12000});
  const sources=await ready;
  if(workerData.action==='init') return {sources};
  const request=workerData.request;
  const source=sources[request?.source];
  if(!source || !source.actions.includes(request.action) || typeof handler!=='function') throw new Error('Source does not support request');
  if(request.action==='musicUrl' && request.source!=='local' && !source.qualitys.includes(request.info?.type)) throw new Error('Source does not support quality');
  const result=await handler(request);
  const serialized=JSON.stringify({result});
  if (!serialized || Buffer.byteLength(serialized)>maxBytes) throw new Error('Script result exceeds limit');
  return JSON.parse(serialized);
}

process.on('uncaughtException',()=>parentPort.postMessage({error:'Source execution failed'}));
process.on('unhandledRejection',()=>parentPort.postMessage({error:'Source execution failed'}));
execute().then(result=>parentPort.postMessage(result)).catch(error=>parentPort.postMessage({
  error:'Source execution failed',timeout:error?.code==='ERR_SCRIPT_EXECUTION_TIMEOUT',
})).finally(()=>clearInterval(lifetime));
