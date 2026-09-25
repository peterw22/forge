const {spawn}=require('node:child_process');
const fs=require('node:fs');
const path=require('node:path');
const root=fs.mkdtempSync('/tmp/pi-go-cli-resume-live-');
const agent=path.resolve('/tmp/pi-go-resume-test');
const env={...process.env,PI_GO_CONFIG_DIR:path.join(root,'config'),PI_GO_PUSH_DISABLED:'true'};
const model='claude/claude-sonnet-5';
let session='';
function start(extra=[]){
 const child=spawn(agent,['--serve','--cwd',root,'--model',model,'--thinking','low',...extra],{env});
 child.stderr.on('data',b=>process.stderr.write(b));
 let buf='',listeners=[];
 child.stdout.on('data',b=>{buf+=b;for(;;){let index=buf.indexOf('\n');if(index<0)break;let text=buf.slice(0,index);buf=buf.slice(index+1);try{let msg=JSON.parse(text);for(let fn of [...listeners])fn(msg)}catch(e){console.error('bad JSON',e)}}});
 function wait(predicate,limit=90000){return new Promise((resolve,reject)=>{let timer=setTimeout(()=>{listeners=listeners.filter(x=>x!==fn);reject(Error('timed out waiting for agent'))},limit);function fn(msg){if(predicate(msg)){clearTimeout(timer);listeners=listeners.filter(x=>x!==fn);resolve(msg)}}listeners.push(fn)})}
 function send(obj){child.stdin.write(JSON.stringify(obj)+'\n')}
 return {child,wait,send,stop:()=>{child.stdin.end();child.kill('SIGTERM')}};
}
(async()=>{
 let a=start();
 let snap=await a.wait(x=>x.command==='get_state');session=snap.session;
 a.send({type:'prompt',id:'one',message:'Remember the code 8043. Reply ACK only.'});
 await a.wait(x=>x.event?.type==='agent_end',90000);
 a.send({type:'new_session',id:'new'});let fresh=await a.wait(x=>x.command==='new_session');
 a.send({type:'switch_session',id:'back',session});let restored=await a.wait(x=>x.command==='switch_session');
 let m=restored.state?.messages||restored.state?.Messages||[];
 console.log('same-process switch history:',m.map(x=>x.content?.filter(y=>y.type==='text').map(y=>y.text).join('')).filter(Boolean));
 let file=path.join(root,'.pi-go','sessions',session+'.jsonl');
 console.log('binding rows after turn:',fs.readFileSync(file,'utf8').split('\n').filter(x=>x.includes('provider_binding')).length);
 a.stop();await new Promise(resolve=>a.child.once('exit',resolve));
 let b=start(['--resume','--session',file]);let resumed=await b.wait(x=>x.command==='get_state');
 let persisted=resumed.state?.messages||resumed.state?.Messages||[];
 console.log('restart history:',persisted.map(x=>x.content?.filter(y=>y.type==='text').map(y=>y.text).join('')).filter(Boolean));
 b.send({type:'prompt',id:'two',message:'What exact code did I ask you to remember? Reply with only the code.'});
 let answer=await b.wait(x=>x.event?.type==='message_end'&&x.event?.message?.role==='assistant',90000);
 console.log('resumed answer:',answer.event.message.content?.filter(x=>x.type==='text').map(x=>x.text).join(''));
 b.stop();console.log('isolated root:',root);
})().catch(err=>{console.error(err);process.exitCode=1});
