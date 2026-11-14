import { createServer } from 'node:http';
import { Worker } from 'node:worker_threads';
import { pathToFileURL } from 'node:url';

const bodyLimit = 2 * 1024 * 1024;
const responseLimit = 4 * 1024 * 1024;

export function createRunnerServer({timeoutMs = 15000, maxConcurrent = 4} = {}) {
  const workers = new Set();
  let admitted = 0;
  const server = createServer(async (request, response) => {
    const reply = (status, value) => {
      if (response.destroyed || response.writableEnded) return;
      let body;
      try { body = JSON.stringify(value); } catch { status = 422; body = '{"error":"Invalid script result"}'; }
      if (Buffer.byteLength(body) > responseLimit) { status = 422; body = '{"error":"Script result exceeds limit"}'; }
      response.writeHead(status, {'Content-Type':'application/json', 'Cache-Control':'no-store'});
      response.end(body);
    };
    if (request.url === '/health' && request.method === 'GET') { reply(200, {status:'ok'}); return; }
    if (request.url !== '/execute') { reply(404, {error:'Unknown endpoint'}); return; }
    if (request.method !== 'POST') { response.setHeader('Allow','POST'); reply(405, {error:'POST required'}); return; }
    if (admitted >= maxConcurrent) { reply(429, {error:'Worker capacity reached'}); return; }
    admitted++;
    let thread;
    let timer;
    const terminate = () => { if (thread) void thread.terminate(); };
    response.on('close', terminate);
    try {
      let length = 0;
      const chunks = [];
      for await (const chunk of request) {
        length += chunk.length;
        if (length > bodyLimit) { reply(413, {error:'Script request exceeds limit'}); return; }
        chunks.push(chunk);
      }
      let payload;
      try { payload = JSON.parse(Buffer.concat(chunks).toString('utf8')); } catch { reply(400,{error:'Invalid JSON'}); return; }
      if (!payload || typeof payload.script !== 'string' || !payload.script.trim() || !['init','request'].includes(payload.action)) {
        reply(400,{error:'Script and supported action required'}); return;
      }
      thread = new Worker(new URL('./execute.mjs',import.meta.url), {
        workerData:payload,
        resourceLimits:{maxOldGenerationSizeMb:48,maxYoungGenerationSizeMb:8,stackSizeMb:2},
      });
      workers.add(thread);
      const result = await new Promise(resolve => {
        timer = setTimeout(() => { resolve({status:504,value:{error:'Source execution timed out'}}); terminate(); },Math.min(15000, Math.max(100,timeoutMs)));
        thread.once('message', message => resolve({status:message.error?(message.timeout?504:422):200,value:message}));
        thread.once('error', () => resolve({status:422,value:{error:'Source execution failed'}}));
        thread.once('exit', () => resolve({status:422,value:{error:'Source execution terminated'}}));
      });
      reply(result.status,result.value);
    } catch {
      reply(400,{error:'Invalid or interrupted request'});
    } finally {
      clearTimeout(timer);
      response.removeListener('close',terminate);
      if (thread) { workers.delete(thread); await thread.terminate(); }
      admitted--;
    }
  });
  server.requestTimeout = 20000;
  server.headersTimeout = 10000;
  server.closeRunner = async () => {
    server.closeAllConnections();
    await Promise.all([...workers].map(worker => worker.terminate()));
    await new Promise(resolve => server.close(resolve));
  };
  return server;
}

if (process.argv[1] && import.meta.url === pathToFileURL(process.argv[1]).href) {
  const server = createRunnerServer();
  server.listen(Number(process.env.PORT || 3000),'0.0.0.0');
  for (const signal of ['SIGTERM','SIGINT']) process.on(signal,()=>{ void server.closeRunner().then(()=>process.exit(0)); });
}
