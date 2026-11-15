import assert from 'node:assert/strict';
import { test } from 'node:test';
import { createServer } from 'node:http';
import { createRunnerServer } from './server.mjs';

// https://fetch.spec.whatwg.org/#port-blocking
const fetchBlockedPorts=new Set([
  0,1,7,9,11,13,15,17,19,20,21,22,23,25,37,42,43,53,69,77,79,87,95,
  101,102,103,104,109,110,111,113,115,117,119,123,135,137,139,143,161,179,
  389,427,465,512,513,514,515,526,530,531,532,540,548,554,556,563,587,601,
  636,989,990,993,995,1719,1720,1723,2049,3659,4045,4190,5060,5061,6000,
  6566,6665,6666,6667,6668,6669,6679,6697,10080,
]);

const source = `
/** @name Fixture */
const {EVENT_NAMES,on,send}=lx;
on(EVENT_NAMES.request, async ({info}) => 'https://example.com/'+info.type);
send(EVENT_NAMES.inited,{sources:{kw:{type:'music',actions:['musicUrl'],qualitys:['128k','flac']}}});`;

async function listen(server) {
  for(let attempt=0;attempt<16;attempt++){
    await new Promise(resolve => server.listen(0, '127.0.0.1', resolve));
    const port=server.address().port;
    if(!fetchBlockedPorts.has(port))return 'http://127.0.0.1:'+port;
    await new Promise(resolve=>server.close(resolve));
  }
  throw new Error('Operating system repeatedly allocated Fetch-blocked test ports');
}

async function withRunner(fn, options = {}) {
  const server = createRunnerServer(options);
  const base = await listen(server);
  try {
    await fn(async body => {
      const response = await fetch(base + '/execute', {method:'POST',body:JSON.stringify(body)});
      return {status:response.status, body:await response.json()};
    });
  } finally {
    await server.closeRunner();
  }
}

test('skips fetch-blocked ports allocated by the operating system',async()=>{
  const server=createServer((request,response)=>response.end('ok'));
  const address=server.address.bind(server);
  let reads=0;
  server.address=()=>reads++===0?{...address(),port:6697}:address();
  try {
    const base=await listen(server);
    assert.ok(reads>=2,'Fetch-blocked allocation must be replaced');
    const response=await fetch(base);
    assert.equal(await response.text(),'ok');
  } finally {
    server.closeAllConnections();
    await new Promise(resolve=>server.close(resolve));
  }
});

test('initializes source declarations and executes an LX request', async () => {
  await withRunner(async run => {
    const init = await run({script:source,action:'init'});
    assert.equal(init.status, 200);
    assert.deepEqual(init.body.sources.kw.qualitys,['128k','flac']);
    const result = await run({script:source,action:'request',request:{source:'kw',action:'musicUrl',info:{type:'128k'}}});
    assert.equal(result.body.result, 'https://example.com/128k');
  });
});

test('supports asynchronous initialization and crypto helpers', async () => {
  const script = source.replace('send(EVENT_NAMES.inited,', 'setTimeout(()=>send(EVENT_NAMES.inited,').replace("}}});", "}}}), 10);")
    .replace("'https://example.com/'+info.type", "lx.utils.crypto.md5('hello')");
  await withRunner(async run => {
    const result=await run({script,action:'request',request:{source:'kw',action:'musicUrl',info:{type:'128k'}}});
    assert.equal(result.status,200);
    assert.equal(result.body.result,'5d41402abc4b2a76b9719d911017c592');
  });
});

test('HTTP requests use only the broker with the ephemeral capability', async () => {
  let calls = 0;
  const broker = createServer(async (req,res) => {
    calls++;
    assert.equal(req.headers.authorization,'Bearer fixture-capability');
    let text='';for await(const chunk of req)text+=chunk;
    assert.equal(JSON.parse(text).url,'https://example.com/song');
    res.setHeader('Content-Type','application/json');
    res.end(JSON.stringify({statusCode:200,headers:{},body:{url:'https://example.com/audio'}}));
  });
  const brokerURL=await listen(broker);
  try {
    await withRunner(async run=>{
      const script=source.replace("async ({info}) => 'https://example.com/'+info.type", `()=>new Promise((resolve,reject)=>lx.request('https://example.com/song',{},(err,resp,body)=>err?reject(err):resolve(body.url)))`);
      const result=await run({script,action:'request',request:{source:'kw',action:'musicUrl',info:{type:'128k'}},broker:{url:brokerURL,token:'fixture-capability'}});
      assert.equal(result.status,200);assert.equal(result.body.result,'https://example.com/audio');assert.equal(calls,1);
    });
  } finally { await new Promise(resolve=>broker.close(resolve)); }
});

test('rejects malformed platform declarations and undeclared quality', async () => {
  await withRunner(async run=>{
    assert.equal((await run({script:source.replace("type:'music'","type:'executable'"),action:'init'})).status,422);
    assert.equal((await run({script:source,action:'request',request:{source:'kw',action:'musicUrl',info:{type:'320k'}}})).status,422);
    assert.equal((await run({script:'lx.send(lx.EVENT_NAMES.inited,{sources:{}})',action:'init'})).status,422);
  });
});

test('negotiates supported capabilities from extended LX source declarations',async()=>{
  const script=source.replace("actions:['musicUrl'],qualitys:['128k','flac']", "actions:['musicUrl','search','lyric','pic'],qualitys:['128k','192k','320k','ultra']")
    .replace("sources:{kw:", "sources:{qs:{type:'music',actions:['musicUrl'],qualitys:['128k']},kw:");
  await withRunner(async run=>{
    const result=await run({script,action:'init'});
    assert.equal(result.status,200);
    assert.deepEqual(Object.keys(result.body.sources),['kw']);
    assert.deepEqual(result.body.sources.kw.actions,['musicUrl']);
    assert.deepEqual(result.body.sources.kw.qualitys,['128k','320k']);
    for(const request of [
      {source:'kw',action:'search',info:{type:'128k'}},
      {source:'kw',action:'musicUrl',info:{type:'192k'}},
      {source:'qs',action:'musicUrl',info:{type:'128k'}},
    ])assert.equal((await run({script,action:'request',request})).status,422);
    assert.equal((await run({script:source.replace("['128k','flac']","['192k']"),action:'init'})).status,422);
  });
});

test('terminates synchronous loops and pending promises without blocking HTTP', async () => {
  await withRunner(async run=>{
    assert.equal((await run({script:'while(true){}',action:'init'})).status,504);
    assert.equal((await run({script:source.replace("async ({info}) => 'https://example.com/'+info.type",'()=>new Promise(()=>{})'),action:'request',request:{source:'kw',action:'musicUrl',info:{type:'128k'}}})).status,504);
    assert.equal((await run({script:source,action:'init'})).status,200);
  }, {timeoutMs:400});
});

test('rejects oversized payloads and unsupported methods', async () => {
  await withRunner(async run=>{
    assert.equal((await run({script:'x'.repeat(2*1024*1024),action:'init'})).status,413);
    assert.equal((await run({script:source,action:'eval'})).status,400);
  });
});

test('script environment omits raw Node and network APIs', async () => {
  const script=source.replace("'https://example.com/'+info.type", "[typeof process,typeof require,typeof fetch,typeof WebSocket].join(',')");
  await withRunner(async run=>{
    const result=await run({script,action:'request',request:{source:'kw',action:'musicUrl',info:{type:'128k'}}});
    assert.equal(result.body.result,'undefined,undefined,undefined,undefined');
  });
});

test('limits concurrent executions and releases capacity on client disconnect', async () => {
  const server=createRunnerServer({timeoutMs:2000,maxConcurrent:1});
  const base=await listen(server);
  const controller=new AbortController();
  try {
    const received=new Promise(resolve=>server.once('request',resolve));
    const stalled=fetch(base+'/execute',{method:'POST',body:JSON.stringify({script:'new Promise(()=>{})',action:'init'}),signal:controller.signal}).catch(()=>null);
    await received;
    const crowded=await fetch(base+'/execute',{method:'POST',body:JSON.stringify({script:source,action:'init'})});
    assert.equal(crowded.status,429);
    controller.abort();
    await stalled;
    let ready;
    for(let attempt=0;attempt<30;attempt++) {
      ready=await fetch(base+'/execute',{method:'POST',body:JSON.stringify({script:source,action:'init'})});
      if(ready.status!==429)break;
      await ready.arrayBuffer();
      await new Promise(resolve=>setTimeout(resolve,10));
    }
    assert.equal(ready.status,200);
  } finally {controller.abort();await server.closeRunner();}
});

test('supports buffer, AES and compression helpers',async()=>{
  const script=source.replace("'https://example.com/'+info.type", `({
    aes:lx.utils.crypto.aesEncrypt(lx.utils.buffer.from('hello'),'aes-128-cbc',lx.utils.buffer.from('1234567890123456'),lx.utils.buffer.from('1234567890123456')).length,
    text:lx.utils.buffer.bufToString(await lx.utils.zlib.inflate(await lx.utils.zlib.deflate(lx.utils.buffer.from('roundtrip'))),'utf8')
  })`);
  await withRunner(async run=>{
    const result=await run({script,action:'request',request:{source:'kw',action:'musicUrl',info:{type:'128k'}}});
    assert.equal(result.status,200);
    assert.deepEqual(result.body.result,{aes:16,text:'roundtrip'});
  });
});

test('matches LX binary-string decoding and ArrayBuffer slice arguments',async()=>{
  const script=source.replace("'https://example.com/'+info.type", `(()=>{
    const array=new Uint8Array([10,20,30,40]);
    return {
      binary:lx.utils.buffer.bufToString(String.fromCharCode(233),'hex'),
      slice:lx.utils.buffer.bufToString(lx.utils.buffer.from(array.buffer,1,2),'hex'),
    };
  })()`);
  await withRunner(async run=>{
    const result=await run({script,action:'request',request:{source:'kw',action:'musicUrl',info:{type:'128k'}}});
    assert.equal(result.status,200);
    assert.deepEqual(result.body.result,{binary:'e9',slice:'141e'});
  });
});

test('reads script metadata from LX license-style headers',async()=>{
  const script=`/*!
 * @name License fixture
 * @version v1.2.1
 * @author Fixture author
 */
if(lx.currentScriptInfo.name!=='License fixture'||lx.currentScriptInfo.version!=='v1.2.1')throw new Error('Missing script metadata');
`+source.replace('/** @name Fixture */','');
  await withRunner(async run=>{
    const result=await run({script,action:'init'});
    assert.equal(result.status,200);
    assert.deepEqual(result.body.sources.kw.qualitys,['128k','flac']);
  });
});

test('matches LX JSON parsing and response shape for text endpoints',async()=>{
  const broker=createServer((req,res)=>{
    req.resume();
    req.on('end',()=>res.end(JSON.stringify({statusCode:200,headers:{'Content-Type':'text/plain'},body:'{"url":"https://example.com/audio"}'})));
  });
  const brokerURL=await listen(broker);
  try {
    await withRunner(async run=>{
      const script=source.replace("async ({info}) => 'https://example.com/'+info.type", `()=>new Promise((resolve,reject)=>lx.request('https://example.com/song',{},(err,resp,body)=>{
        if(err)return reject(err);
        resolve({url:body.url,contentType:resp.headers['content-type'],raw:resp.raw.toString('utf8'),bytes:resp.bytes});
      }))`);
      const result=await run({script,action:'request',request:{source:'kw',action:'musicUrl',info:{type:'128k'}},broker:{url:brokerURL,token:'fixture-capability'}});
      assert.equal(result.status,200);
      assert.deepEqual(result.body.result,{url:'https://example.com/audio',contentType:'text/plain',raw:'{"url":"https://example.com/audio"}',bytes:35});
    });
  } finally {await new Promise(resolve=>broker.close(resolve));}
});
