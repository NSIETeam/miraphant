var repos=[
{id:'otto',name:'Otto',badge:'rb-otto',color:'#00E5FF'},
{id:'circle',name:'Circle',badge:'rb-circle',color:'#2E7D32'},
{id:'OliveWolf',name:'OliveWolf',badge:'rb-olivewolf',color:'#E65100'},
{id:'miraphant',name:'Miraphant',badge:'rb-miraphant',color:'#6A1B9A'},
{id:'EasyHermes',name:'EasyHermes',badge:'rb-easyhermes',color:'#1565C0'}
];
var curRepo='otto';
var DB={get:function(k,d){try{var v=localStorage.getItem('fu-'+k);return v?JSON.parse(v):d}catch(e){return d}},set:function(k,v){localStorage.setItem('fu-'+k,JSON.stringify(v))}};
var cu={name:'Chen Xue',dept:'Design',role:'Design Director',id:'MF-004'};
function nowTS(){var d=new Date();function p(n){return String(n).padStart(2,'0')}return d.getFullYear()+'-'+p(d.getMonth()+1)+'-'+p(d.getDate())+' '+p(d.getHours())+':'+p(d.getMinutes())}
function todayDate(){return new Date().toISOString().slice(0,10)}
function seed(){
repos.forEach(function(r){
if(!DB.get('entries-'+r.id)){
var seedData=getSeed(r.id);
if(seedData.length>0)DB.set('entries-'+r.id,seedData);
}
});
}
function getSeed(repo){
var seeds={
otto:[
{ts:'2026-07-06 06:52',author:'krx521920',status:'done',content:'Mem0 structured memory + LangGraph task orchestration + OR-Tools optimization deployed. Multi-agent collaboration active.',next:'Monitor memory retrieval accuracy in production'},
{ts:'2026-07-06 06:28',author:'NSIETeam',status:'on',content:'Feishu project collaboration actions extended. Planner framework added.',next:'Ship collaboration planner v2 with task assignment'},
{ts:'2026-07-06 01:00',author:'NSIETeam',status:'on',content:'Codebase memory MCP adapter + CLI project memory commands wired to org memory MVP.',next:'Connect MCP tools to project memory manager'}
],
circle:[
{ts:'2026-07-03 14:00',author:'NSIETeam',status:'on',content:'AI-driven property matching algorithm v2 deployed. B2B platform ready for pilot.',next:'Onboard first 3 enterprise clients for beta'}
],
OliveWolf:[
{ts:'2026-07-04 11:00',author:'Felix201209',status:'plan',content:'Dual render backends working. Real-time audio-driven conversation functional.',next:'Benchmark latency on enterprise GPU (RTX 4090)'}
],
miraphant:[
{ts:'2026-07-07 12:10',author:'NSIETeam',status:'on',content:'Employee portal with OA system, follow-up tracker, and contribution log. Password auth implemented.',next:'Deploy to GitHub Pages + test auth flow on mobile'}
],
EasyHermes:[
{ts:'2026-07-02 09:00',author:'krx521920',status:'hold',content:'Core pipeline optimization paused pending Otto memory module.',next:'Resume after Otto v1.4 release'}
]
};
return seeds[repo]||[];
}
function sBg(s){var m={on:'bg-on',hold:'bg-hold',done:'bg-done',risk:'bg-risk',plan:'bg-plan'};var l={on:'Active',hold:'On Hold',done:'Done',risk:'At Risk',plan:'Planned'};return'<span class="bg '+(m[s]||'bg-plan')+'">'+l[s]+'</span>'}
function toast(m){var e=document.getElementById('toast');if(!e){e=document.createElement('div');e.id='toast';e.className='toast';document.body.appendChild(e)}e.textContent=m;e.classList.add('show');setTimeout(function(){e.classList.remove('show')},2500)}
function renderStats(){
var total=0,active=0,done=0,risk=0,hold=0;
repos.forEach(function(r){var d=DB.get('entries-'+r.id,[]);total+=d.length;if(d.length>0){var last=d[0];if(last.status==='on')active++;if(last.status==='done')done++;if(last.status==='risk')risk++;if(last.status==='hold')hold++}});
var s=[{n:repos.length,l:'Projects'},{n:active,l:'Active'},{n:done,l:'Done'},{n:total,l:'Total Entries'}];
document.getElementById('fuStats').innerHTML=s.map(function(x){return'<div class="stat-box"><div class="num">'+x.n+'</div><div class="lbl">'+x.l+'</div></div>'}).join('');
}
function renderTabs(){
document.getElementById('repoTabs').innerHTML=repos.map(function(r){
return'<button class="repo-tab '+(curRepo===r.id?'active':'')+'" onclick="switchRepo(\''+r.id+'\')"><span class="repo-badge '+r.badge+'">'+r.name+'</span></button>'
}).join('');
}
function switchRepo(id){curRepo=id;renderProject()}
function renderProject(){
var r=repos.find(function(x){return x.id===curRepo});
var entries=DB.get('entries-'+curRepo,[]);
var lastStatus=entries.length>0?entries[0].status:'plan';
var html='<div class="project-header reveal visible">'+
'<div class="project-title"><span class="repo-badge '+r.badge+'">'+r.name+'</span> <span style="color:var(--g400);font-size:14px;font-weight:400">'+entries.length+' entries / last: '+sBg(lastStatus)+'</span></div>'+
'</div>';
/* Entry form */
html+='<div class="tw" style="margin-bottom:24px;padding:20px">'+
'<div style="font-size:12px;font-weight:600;margin-bottom:12px;color:var(--g600)">Add Update (auto-timestamped)</div>'+
'<div class="fi-row">'+
'<input type="text" class="fi-input" id="newContent" placeholder="What happened? (status update, milestone, blocker...)">'+
'<select class="fi-select" id="newStatus"><option value="on">Active</option><option value="done">Done</option><option value="plan">Planned</option><option value="hold">On Hold</option><option value="risk">At Risk</option></select>'+
'<input type="text" class="fi-input" id="newNext" placeholder="Next step (optional)" style="max-width:200px">'+
'<button class="btn-p btn-sm" onclick="addEntry()">+ Add</button>'+
'</div>'+
'<div style="font-size:11px;color:var(--g400);margin-top:8px">Timestamp: <span class="mono-ts">'+nowTS()+'</span> / Author: '+cu.name+'</div>'+
'</div>';
/* Entries table */
html+='<div class="tw"><div class="ts"><table><thead><tr><th style="width:140px">Timestamp</th><th style="width:80px">Author</th><th style="width:80px">Status</th><th>Update</th><th>Next Step</th><th style="width:60px"></th></tr></thead><tbody>';
if(entries.length===0){
html+='<tr><td colspan="6"><div class="empty">No entries yet. Add the first update above.</div></td></tr>';
}else{
entries.forEach(function(e){
html+='<tr>'+
'<td class="mono-ts">'+e.ts+'</td>'+
'<td style="font-size:12px">'+e.author+'</td>'+
'<td>'+sBg(e.status)+'</td>'+
'<td style="max-width:300px">'+e.content+'</td>'+
'<td style="max-width:200px;color:var(--g500);font-size:12px">'+(e.next||'—')+'</td>'+
'<td><button class="btn-gh" onclick="delEntry(\''+e.ts+'\')">Delete</button></td>'+
'</tr>';
});
}
html+='</tbody></table></div></div>';
document.getElementById('projectArea').innerHTML=html;
/* Update timestamp live */
setInterval(function(){var ts=document.querySelector('.mono-ts');if(ts&&ts.id==='')ts.textContent=nowTS()},30000);
}
function addEntry(){
var content=document.getElementById('newContent').value.trim();
if(!content){toast('Please enter update content');return}
var status=document.getElementById('newStatus').value;
var next=document.getElementById('newNext').value.trim();
var entries=DB.get('entries-'+curRepo,[]);
entries.unshift({ts:nowTS(),author:cu.name,status:status,content:content,next:next});
DB.set('entries-'+curRepo,entries);
renderStats();renderProject();
toast('Entry added');
}
function delEntry(ts){
var entries=DB.get('entries-'+curRepo,[]).filter(function(x){return x.ts!==ts});
DB.set('entries-'+curRepo,entries);
renderStats();renderProject();
toast('Deleted');
}
function renderAll(){renderStats();renderTabs();renderProject()}
var nav=document.getElementById('nav');window.addEventListener('scroll',function(){nav.classList.toggle('scrolled',window.scrollY>40)},{passive:true});
document.getElementById('navToggle').addEventListener('click',function(){document.getElementById('navLinks').classList.toggle('open')});
document.querySelectorAll('a[href$=".html"]').forEach(function(a){a.addEventListener('click',function(e){var h=a.getAttribute('href');if(h&&!h.startsWith('http')){e.preventDefault();document.body.style.opacity='0';document.body.style.transition='opacity .15s';setTimeout(function(){window.location.href=h},150)}})});
seed();renderAll();
