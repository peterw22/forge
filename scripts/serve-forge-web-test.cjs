const http = require('node:http');
const fs = require('node:fs');
const path = require('node:path');
const root = path.resolve(process.argv[2]);
const types = {'.html':'text/html; charset=utf-8','.js':'application/javascript','.mjs':'application/javascript','.json':'application/json','.css':'text/css','.wasm':'application/wasm','.png':'image/png','.svg':'image/svg+xml','.ttf':'font/ttf'};
http.createServer((req,res) => {
  let name;
  try { name = decodeURIComponent(new URL(req.url, 'http://localhost').pathname); } catch {res.writeHead(400);return res.end();}
  const file = path.resolve(root, '.' + name);
  if (file !== root && !file.startsWith(root + path.sep)) { res.writeHead(403); return res.end(); }
  const target = fs.existsSync(file) && fs.statSync(file).isFile() ? file : path.join(root,'index.html');
  res.setHeader('Content-Type', types[path.extname(target)] || 'application/octet-stream');
  fs.createReadStream(target).on('error', () => {res.writeHead(404);res.end();}).pipe(res);
}).listen(Number(process.argv[3]), '127.0.0.1');
