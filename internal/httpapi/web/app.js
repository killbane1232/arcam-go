'use strict';
const $ = id => document.getElementById(id);
const esc = v => String(v ?? '').replace(/[&<>"']/g, c => ({'&':'&amp;','<':'&lt;','>':'&gt;','"':'&quot;',"'":'&#39;'}[c]));
const clone = v => JSON.parse(JSON.stringify(v));
const num = (v,d=2) => Number(v || 0).toLocaleString('en-US',{maximumFractionDigits:d,minimumFractionDigits:d});
const date = v => new Date(v).toISOString().replace('T',' ').slice(0,16);
const symbols = id => $(id).value.split(',').map(s=>s.trim().toUpperCase()).filter(Boolean);
const utc = id => new Date($(id).value+'Z').toISOString();
let datasetInfo=[], connectionSettings=[], catalog, versions=[], runs=[], currentRun=null, pair=null, draft, decisionPage=0, viewStart=0, viewCount=0, pollTimer, selectedRunID='', runSelectionEpoch=0;
function notice(message,error=false){$('notice').textContent=message;$('notice').className=error?'error':'';$('notice').hidden=false;clearTimeout(notice.timer);notice.timer=setTimeout(()=>$('notice').hidden=true,error?20000:8000);}
async function api(path,options={}){const headers={'Content-Type':'application/json',...options.headers};const token=sessionStorage.getItem('arcam-token');if(token)headers.Authorization='Bearer '+token;const res=await fetch(path,{...options,headers});if(!res.ok){let e;try{e=await res.json();}catch{e={error:res.statusText};}throw Error(e.error||'HTTP '+res.status);}return options.blob?res.blob():res.json();}
const post=(path,value={})=>api(path,{method:'POST',body:JSON.stringify(value)});
function safe(fn){return async(...args)=>{try{return await fn(...args);}catch(e){notice(e.message,true);}};}
function selectOptions(el,items){const old=el.value;el.replaceChildren(...items.map(([value,label])=>{const o=document.createElement('option');o.value=value;o.textContent=label;return o;}));if(items.some(x=>x[0]===old))el.value=old;}
function showDetail(title,value){$('detail-title').textContent=title;$('detail-content').textContent=JSON.stringify(value,null,2);$('detail-dialog').showModal();}
function download(name,value,type='application/json'){const blob=value instanceof Blob?value:new Blob([JSON.stringify(value,null,2)],{type});const a=document.createElement('a');a.href=URL.createObjectURL(blob);a.download=name;a.click();setTimeout(()=>URL.revokeObjectURL(a.href),1000);}
function switchTab(name){document.querySelectorAll('.tab').forEach(e=>e.classList.toggle('active',e.id==='tab-'+name));document.querySelectorAll('nav button').forEach(e=>e.classList.toggle('active',e.dataset.tab===name));$('page-title').textContent={lab:'Лаборатория стратегий',strategies:'Конструктор стратегий',data:'История рынка',sessions:'Торговые сессии',settings:'Настройки подключения',mcp:'Интерфейс для агентов'}[name];if(name==='data')safe(loadData)();if(name==='settings'&&!$('connection-settings').children.length)safe(loadSettings)();if(name==='sessions')safe(loadSessions)();if(name==='lab')drawCharts();}
document.querySelectorAll('nav button').forEach(e=>e.onclick=()=>switchTab(e.dataset.tab));
$('close-dialog').onclick=()=>$('detail-dialog').close();
$('auth-button').onclick=()=>{const token=prompt('API-токен сервера (хранится только в этой вкладке)',sessionStorage.getItem('arcam-token')||'');if(token!==null){sessionStorage.setItem('arcam-token',token);safe(init)();}};
async function loadStrategies(){versions=await api('/api/strategies');const opts=versions.map(v=>[v.id+':'+v.version,v.strategy.name+' · v'+v.version]);for(const id of ['run-strategy','session-strategy']){const el=$(id),hadSelection=!!el.value;selectOptions(el,opts);if(!hadSelection&&opts.some(([value])=>value==='trend-demo:1'))el.value='trend-demo:1';}$('strategy-list').innerHTML=versions.slice().reverse().map(v=>`<div class="version-card"><button data-version="${esc(v.id)}:${v.version}">${esc(v.strategy.name)} <span class="badge">v${v.version}</span><small>${esc(v.id)} · ${date(v.created_at)}</small><small>${esc(v.hash.slice(0,16))}</small></button></div>`).join('');$('strategy-list').querySelectorAll('button').forEach(b=>b.onclick=()=>{const v=versions.find(v=>v.id+':'+v.version===b.dataset.version);setDraft(v.strategy,v.id,'На основе v'+v.version+' → сохранится новая версия');});}
async function loadRuns(){runs=await api('/api/runs');selectOptions($('run-select'),runs.map(r=>[r.id,date(r.created_at)+' · '+r.strategy.strategy.name+' v'+r.strategy.version+' · '+r.status]));}
const activeRun = r => ['queued','preparing','running'].includes(r.status);
function renderRunStatus(r){
 $('run-status').textContent=r.status.toUpperCase();
 const selected=$('run-select').selectedOptions[0];
 if(selected&&selected.value===r.id)selected.textContent=date(r.created_at)+' · '+r.strategy.strategy.name+' v'+r.strategy.version+' · '+r.status;
 $('resume-run').hidden=r.status!=='failed';$('cancel-run').hidden=!activeRun(r);$('download-run').hidden=!(r.pairs||[]).some(p=>p.candles?.length);$('download-run').textContent=r.status==='completed'?'↓ JSON отчёт':'↓ Промежуточный JSON';
 $('run-telemetry').hidden=false;
 const p=r.backtest_progress, done=r.status==='completed';
 const bar=$('run-progress-bar');
 if(p&&r.status!=='queued'&&r.status!=='preparing'||done){
  const percent=done?100:Math.min(100,100*p.processed_candles/Math.max(1,p.total_candles));
  bar.value=percent;$('run-percentage').textContent=num(Math.floor(percent*10)/10,1)+'%';
 }else{bar.removeAttribute('value');$('run-percentage').textContent={queued:'В очереди',preparing:'Загрузка истории',running:'Выполняется'}[r.status]||'Нет замеров';}
 const processed=p?.processed_candles??(done?(r.pairs||[]).reduce((n,p)=>n+(p.decisions?.length||0),0):null);
 $('run-candles').textContent=p?`${num(processed,0)} / ${num(p.total_candles,0)} свечей · все пары`:(processed!==null?`${num(processed,0)} свечей обработано`:'Прогресс для этого запуска не записывался');
 const phase={queued:'Ожидание свободного исполнителя',preparing:'Подготовка данных и прогрева',running:'Расчёт стратегии',completed:'Расчёт завершён',cancelled:'Расчёт отменён',failed:'Расчёт остановлен с ошибкой',interrupted:'Расчёт прерван'}[r.status]||r.status;
 $('run-current').textContent=[phase,p?.current_symbol,p?.last_candle_time?'Последняя свеча: '+date(p.last_candle_time)+' UTC':''].filter(Boolean).join(' · ');
 $('run-progress').textContent=r.progress||'';$('run-progress').hidden=!r.progress;
 $('run-llm-stats').hidden=!r.strategy.strategy.llm;
 const stats=r.llm_stats;
 $('run-llm-average').textContent=stats?.responses?formatResponseTime(stats.average_response_ms):'—';
 $('run-llm-counts').textContent=stats?`${num(stats.responses,0)} ответов · ${num(stats.cache_hits,0)} из кэша · ${num(stats.skipped,0)} пропущено`:'Время ответов в этом запуске не записывалось';
 $('run-llm-average').title='От отправки HTTP-запроса до получения ответа. Только успешные запросы этого запуска; кэш и ошибки не учитываются.';
}
function formatResponseTime(ms){return ms<1000?num(ms,0)+' мс':num(ms/1000,2)+' с';}
function resultCursor(p){return {candles:(p.candles||[]).length,fills:(p.fills||[]).length,trades:(p.trades||[]).length};}
function sameCursor(a,b){return a.candles===b.candles&&a.fills===b.fills&&a.trades===b.trades;}
function mergeRunUpdate(update){
 const oldPairs=currentRun?.pairs||[];
 // Reject an out-of-order response before changing any local array.
 for(const d of update.pairs){
  const old=oldPairs.find(p=>p.symbol===d.pair.symbol);
  if(!d.reset&&!sameCursor(resultCursor(old||{}),d.from))return false;
 }
 const pairs=oldPairs.slice();
 for(const d of update.pairs){
  const index=pairs.findIndex(p=>p.symbol===d.pair.symbol);
  const old=d.reset?{}:(pairs[index]||{});
  const p={...d.pair};
  for(const key of ['candles','decisions','equity','fills','trades'])p[key]=d.pair[key]?.length?(old[key]||[]).concat(d.pair[key]):(old[key]||[]);
  if(index<0)pairs.push(p);else pairs[index]=p;
 }
 currentRun={...currentRun,...update.run,pairs};
 return true;
}
function scheduleRunPoll(id,epoch=runSelectionEpoch){if(selectedRunID!==id||epoch!==runSelectionEpoch)return;clearTimeout(pollTimer);if(currentRun&&activeRun(currentRun))pollTimer=setTimeout(()=>pollRun(id,epoch),1200);}
async function pollRun(id,epoch){
 try{
  const after=Object.fromEntries((currentRun.pairs||[]).map(p=>[p.symbol,resultCursor(p)]));
  const update=await api('/api/runs/'+encodeURIComponent(id)+'/updates?after='+encodeURIComponent(JSON.stringify(after)));
  if(selectedRunID!==id||epoch!==runSelectionEpoch)return;
  const priorStatus=currentRun.status;
  const changed=priorStatus!==update.run.status||currentRun.backtest_progress?.current_symbol!==update.run.backtest_progress?.current_symbol||update.pairs.some(d=>d.reset||!sameCursor(d.from,d.next)||!(currentRun.pairs||[]).some(p=>p.symbol===d.pair.symbol));
  if(!mergeRunUpdate(update))return;
  renderRunStatus(currentRun);if(changed)renderRunResults(true);
  if(priorStatus!==currentRun.status){
   if(currentRun.error)notice(currentRun.error,true);
   await loadRuns();
   if(currentRun.status==='completed'&&currentRun.data_loads?.length)await loadData();
  }
 }catch(e){if(selectedRunID===id&&epoch===runSelectionEpoch)notice(e.message,true);}
 finally{scheduleRunPoll(id,epoch);}
}
async function selectRun(id){
 if(!id)return;selectedRunID=id;const epoch=++runSelectionEpoch;clearTimeout(pollTimer);
 const sameRun=currentRun?.id===id;
 const update=await api('/api/runs/'+encodeURIComponent(id)+'/updates');
 if(selectedRunID!==id||epoch!==runSelectionEpoch)return;
 currentRun={...update.run,pairs:update.pairs.map(d=>({...d.pair,...Object.fromEntries(['candles','decisions','equity','fills','trades'].map(k=>[k,d.pair[k]||[]]))}))};
 if(!sameRun){$('follow-run').checked=true;decisionPage=0;}
 $('run-select').value=id;renderRunStatus(currentRun);
 if(currentRun.error)notice(currentRun.error,true);
 renderRunResults(sameRun);scheduleRunPoll(id,epoch);
}
function renderRunResults(preserve){
 const pairs=currentRun?.pairs||[],oldSymbol=pair?.symbol;
 selectOptions($('chart-pair'),pairs.map(p=>[p.symbol,p.symbol+(p.complete?' · готово':'' )]));
 if($('follow-run').checked&&pairs.some(p=>p.symbol===currentRun.backtest_progress?.current_symbol))$('chart-pair').value=currentRun.backtest_progress.current_symbol;
 selectPair(preserve&&oldSymbol===$('chart-pair').value);
}
function selectPair(preserve=false){
 const oldOffset=+$('chart-offset').value;
 pair=(currentRun?.pairs||[]).find(p=>p.symbol===$('chart-pair').value)||null;
 if(!preserve)decisionPage=0;
 const strategy=currentRun?.strategy.strategy;
 const fields=strategy?[...(strategy.indicators||[]).flatMap(i=>(outputs[i.type]||[]).map(o=>i.id+'.'+o)),...(strategy.llm?['enter_long','exit_long','enter_short','exit_short'].map(k=>'llm.'+k):[])]:[];
 selectOptions($('chart-feature'),[['','Без индикатора'],...fields.map(k=>[k,k])]);
 if(pair){
  $('chart-caption').textContent=`${pair.symbol} · ${currentRun.request.interval} · ${currentRun.request.source==='demo'?'СИНТЕТИЧЕСКИЕ ДАННЫЕ':currentRun.request.source} · ${pair.candles.length} свечей`;
  const max=Math.max(0,pair.candles.length-+$('chart-window').value);
  $('chart-offset').max=max;
  $('chart-offset').value=$('follow-run').checked||!preserve?max:Math.min(oldOffset,max);
 }
 const partial=currentRun&&currentRun.status!=='completed';
 const metrics=partial?pair?.metrics:currentRun?.metrics;
 renderMetrics(metrics,partial);
 $('run-results-note').hidden=!currentRun;
 $('run-results-note').textContent=partial?(pair?.candles.length?`Промежуточные результаты · ${pair.symbol} · по ${date(pair.decisions.at(-1).time)} UTC. Метрики этой пары, win rate — по закрытым сделкам.`:'Результаты появятся после первой обработанной свечи.'): 'Итоговые результаты · все пары';
 const position=pair?.open_position;
 $('run-position').hidden=!partial||!pair?.candles.length;
 $('run-position').textContent=position?`Открыта ${position.side.toUpperCase()} · вход ${num(position.entry)} · количество ${num(position.quantity,6)} · нереализованный PnL ${num(pair.unrealized_pnl)}${pair.pending_action&&pair.pending_action!=='hold'?' · На следующем открытии: '+pair.pending_action:''}`:(pair?.pending_action&&pair.pending_action!=='hold'?'На следующем открытии: '+pair.pending_action:'Позиция не открыта');
 drawCharts();renderDecisions();renderTrades();
}

function renderMetrics(m,partial=false){if(!m||!m.initial){$('metrics').innerHTML='';return;}const cards=[['ДОХОДНОСТЬ',num(m.return_pct)+'%',m.return_pct>=0?'green':'red'],[partial?'КАПИТАЛ ПАРЫ':'КОНЕЧНЫЙ КАПИТАЛ',num(m.final),''],['MAX DRAWDOWN',num(m.max_drawdown_pct)+'%','red'],['СДЕЛКИ / WIN RATE',m.trades+' / '+num(m.win_rate_pct,1)+'%',''],['КОМИССИИ',num(m.fees),'']];$('metrics').innerHTML=cards.map(([label,value,color])=>`<div class="metric"><small>${label}</small><strong class="${color}">${value}</strong></div>`).join('');}
$('run-form').onsubmit=safe(async e=>{e.preventDefault();const [strategy_id,version]=$('run-strategy').value.split(':');const button=$('run-submit');button.disabled=true;try{const r=await post('/api/runs',{strategy_id,version:+version,source:$('run-source').value,symbols:symbols('run-symbols'),interval:$('run-interval').value,from:utc('run-from'),to:utc('run-to'),capital:+$('run-capital').value,fee_bps:+$('run-fee').value,slippage_bps:+$('run-slippage').value,allow_llm:$('run-llm').checked,max_llm_calls:+$('run-budget').value});await loadRuns();await selectRun(r.id);}finally{button.disabled=false;}});
$('resume-run').onclick=safe(async()=>{
 const id=currentRun.id,button=$('resume-run');button.disabled=true;
 try{
  const resumed=await post('/api/runs/'+encodeURIComponent(id)+'/resume');
  await loadRuns();
  if(selectedRunID===id){
   await selectRun(id);
   notice(resumed.resume_note?'Продолжен тот же запуск. Для старого отчёта подключение LLM зафиксировано из текущих настроек; параметры стратегии и теста сохранены.':'Продолжен тот же запуск с последней обработанной свечи.');
  }
 }finally{button.disabled=false;}
});
$('run-select').onchange=safe(()=>selectRun($('run-select').value));$('cancel-run').onclick=safe(()=>post('/api/runs/'+currentRun.id+'/cancel'));$('download-run').onclick=safe(async()=>{const id=currentRun.id;download('arcam-'+id+'.json',await api('/api/runs/'+encodeURIComponent(id)));});
$('chart-pair').onchange=()=>{$('follow-run').checked=false;selectPair();};
$('follow-run').onchange=()=>renderRunResults(true);
$('chart-feature').onchange=drawCharts;
$('chart-offset').oninput=()=>{$('follow-run').checked=false;drawCharts();};
$('chart-window').onchange=()=>selectPair(true);

function canvas(id){const c=$(id),w=c.clientWidth,h=Number(c.dataset.logicalHeight||c.getAttribute('height'))||100,dpr=devicePixelRatio||1;c.dataset.logicalHeight=h;c.width=w*dpr;c.height=h*dpr;c.style.height=h+'px';const ctx=c.getContext('2d');ctx.scale(dpr,dpr);ctx.clearRect(0,0,w,h);return{c,ctx,w,h};}
function grid(ctx,w,h,lo,hi,label){ctx.font='10px ui-monospace, monospace';ctx.fillStyle='#8e9db2';ctx.fillText(label,8,15);for(let n=0;n<5;n++){const y=25+n*(h-50)/4;ctx.strokeStyle='#243041';ctx.beginPath();ctx.moveTo(0,y);ctx.lineTo(w-75,y);ctx.stroke();ctx.fillStyle='#8e9db2';ctx.fillText(num(hi-(hi-lo)*n/4,2),w-69,y+3);}}
function lineChart(id,values,label,color){const{ctx,w,h}=canvas(id);const valid=values.filter(Number.isFinite);if(!valid.length){ctx.fillStyle='#8e9db2';ctx.font='11px sans-serif';ctx.fillText(label+' · нет значений',8,25);return;}let lo=Math.min(...valid),hi=Math.max(...valid);if(lo===hi){lo-=1;hi+=1;}grid(ctx,w,h,lo,hi,label);ctx.strokeStyle=color;ctx.lineWidth=1.5;ctx.beginPath();let started=false;values.forEach((v,i)=>{if(!Number.isFinite(v)){started=false;return;}const x=(i+.5)*(w-80)/values.length,y=25+(hi-v)/(hi-lo)*(h-50);if(!started){ctx.moveTo(x,y);started=true;}else ctx.lineTo(x,y);});ctx.stroke();}
function drawCharts(){if(!$('tab-lab').classList.contains('active'))return;$('chart-empty').hidden=!!pair?.candles?.length;const{ctx,w,h}=canvas('price-chart');if(!pair?.candles?.length){lineChart('indicator-chart',[],'Индикатор','#e5bf7b');lineChart('equity-chart',[],'Капитал','#9be1c0');return;}viewStart=+$('chart-offset').value;viewCount=Math.min(+$('chart-window').value,pair.candles.length-viewStart);const bars=pair.candles.slice(viewStart,viewStart+viewCount);if(!bars.length)return;let lo=Math.min(...bars.map(c=>c.low)),hi=Math.max(...bars.map(c=>c.high));const pad=(hi-lo)*.07||1;lo-=pad;hi+=pad;grid(ctx,w,h,lo,hi,'OHLC');const step=(w-80)/bars.length,x=i=>(i+.5)*step,y=p=>25+(hi-p)/(hi-lo)*(h-50);bars.forEach((c,i)=>{const color=c.close>=c.open?'#8ed9b8':'#eb8c9f';ctx.strokeStyle=color;ctx.fillStyle=color;ctx.beginPath();ctx.moveTo(x(i),y(c.high));ctx.lineTo(x(i),y(c.low));ctx.stroke();ctx.fillRect(x(i)-Math.max(.8,step*.55)/2,Math.min(y(c.open),y(c.close)),Math.max(.8,step*.55),Math.max(1,Math.abs(y(c.open)-y(c.close))));const d=pair.decisions[viewStart+i];ctx.fillStyle=d.action==='hold'?'#41536c':d.action.includes('short')?'#f48c9b':'#9be1c0';ctx.beginPath();ctx.arc(x(i),h-10,d.action==='hold'?1.2:2.7,0,Math.PI*2);ctx.fill();});const stepMs=bars.length>1?new Date(bars[1].time)-new Date(bars[0].time):3600000;const start=new Date(bars[0].time).getTime();pair.fills.forEach(f=>{const idx=Math.min(bars.length-1,Math.floor((new Date(f.time).getTime()-start)/stepMs));if(idx<0||new Date(f.time).getTime()>start+bars.length*stepMs)return;const xx=x(idx),yy=y(f.price);ctx.fillStyle=f.action.startsWith('exit')?'#e5bf7b':f.action.includes('long')?'#9be1c0':'#f48c9b';ctx.beginPath();if(f.action.startsWith('exit')){ctx.moveTo(xx,yy-6);ctx.lineTo(xx+5,yy);ctx.lineTo(xx,yy+6);ctx.lineTo(xx-5,yy);}else{const dir=f.action==='enter_long'?1:-1;ctx.moveTo(xx,yy-dir*7);ctx.lineTo(xx-5,yy+dir*4);ctx.lineTo(xx+5,yy+dir*4);}ctx.closePath();ctx.fill();});ctx.fillStyle='#8e9db2';ctx.fillText(date(bars[0].time),4,h-22);ctx.fillText(date(bars.at(-1).time),Math.max(160,w-210),h-22);const feature=$('chart-feature').value;lineChart('indicator-chart',pair.decisions.slice(viewStart,viewStart+viewCount).map(d=>d.features[feature]),feature||'Выберите индикатор','#e5bf7b');lineChart('equity-chart',pair.equity.slice(viewStart,viewStart+viewCount).map(e=>e.value),'КАПИТАЛ ПАРЫ','#9be1c0');}
$('price-chart').onmousemove=e=>{if(!pair?.candles?.length)return;const r=e.target.getBoundingClientRect();const i=viewStart+Math.min(viewCount-1,Math.max(0,Math.floor((e.clientX-r.left)/(r.width-80)*viewCount)));const c=pair.candles[i],d=pair.decisions[i];if(!c)return;$('chart-tooltip').textContent=`${date(c.time)} UTC\nO ${num(c.open)}  H ${num(c.high)}  L ${num(c.low)}  C ${num(c.close)}\n${d.action} · ${d.reason}\nНажмите для дерева условий`;$('chart-tooltip').hidden=false;};$('price-chart').onmouseleave=()=>$('chart-tooltip').hidden=true;$('price-chart').onclick=e=>{if(!pair?.candles?.length)return;const r=e.target.getBoundingClientRect(),i=viewStart+Math.min(viewCount-1,Math.max(0,Math.floor((e.clientX-r.left)/(r.width-80)*viewCount)));showDetail('Решение · '+pair.symbol,pair.decisions[i]);};
window.addEventListener('resize',drawCharts);
function renderDecisions(){const list=(pair?.decisions||[]).filter(d=>$('decision-filter').value==='all'||d.action!=='hold');decisionPage=$('follow-run').checked?Math.max(0,Math.ceil(list.length/30)-1):Math.min(decisionPage,Math.max(0,Math.ceil(list.length/30)-1));const page=list.slice(decisionPage*30,(decisionPage+1)*30);$('page-label').textContent=`${list.length?decisionPage+1:0} / ${Math.ceil(list.length/30)}`;$('decisions').innerHTML=page.map((d,i)=>`<tr><td>${date(d.time)}</td><td class="${d.action.includes('short')?'red':d.action==='hold'?'':'green'}"><code>${esc(d.action)}</code></td><td>${num(d.features.close)}</td><td class="reason">${esc(d.llm?.verdict.reason||(d.reason+(d.llm_status==='skipped'?' · LLM не требовалась':'')))}</td><td><button data-index="${i}" class="subtle">Условия ↗</button></td></tr>`).join('');$('decisions').querySelectorAll('button').forEach(b=>b.onclick=()=>showDetail('Дерево условий и LLM',page[+b.dataset.index]));}
function renderTrades(){$('trades').innerHTML=(pair?.trades||[]).map(t=>`<tr><td class="${t.side==='long'?'green':'red'}">${esc(t.side)}</td><td>${date(t.entry_time)}</td><td>${date(t.exit_time)}</td><td>${num(t.entry)} → ${num(t.exit)}</td><td class="${t.pnl>=0?'green':'red'}">${num(t.pnl)}</td><td>${esc(t.reason)}</td></tr>`).join('');}
$('decision-filter').onchange=()=>{decisionPage=0;renderDecisions();};$('prev-page').onclick=()=>{$('follow-run').checked=false;decisionPage=Math.max(0,decisionPage-1);renderDecisions();};$('next-page').onclick=()=>{$('follow-run').checked=false;decisionPage++;renderDecisions();};

function setDraft(strategy,id,label){draft=clone(strategy);$('strategy-id').value=id;$('editing-version').textContent=label;$('strategy-name').value=draft.name;$('strategy-description').value=draft.description;$('risk-fraction').value=draft.risk.position_fraction;$('risk-stop').value=draft.risk.stop_loss;$('risk-take').value=draft.risk.take_profit;$('llm-enabled').checked=!!draft.llm;$('llm-config').hidden=!draft.llm;if(draft.llm){$('llm-provider').value=draft.llm.provider;$('llm-protocol').value=draft.llm.protocol||'chat';$('llm-model').value=draft.llm.model;$('llm-lookback').value=draft.llm.lookback;$('llm-instructions').value=draft.llm.instructions;}else{$('llm-protocol').value='chat';$('llm-model').value='';$('llm-instructions').value='';}renderBuilder();}
function readDraft(){draft.name=$('strategy-name').value;draft.description=$('strategy-description').value;draft.risk={position_fraction:+$('risk-fraction').value,stop_loss:+$('risk-stop').value,take_profit:+$('risk-take').value};if($('llm-enabled').checked)draft.llm={provider:$('llm-provider').value,model:$('llm-model').value,...($('llm-protocol').value==='systemone'?{protocol:'systemone'}:{}),lookback:+$('llm-lookback').value,instructions:$('llm-instructions').value};else delete draft.llm;return draft;}
function syncJSON(){$('strategy-json').value=JSON.stringify(readDraft(),null,2);}
const params={ema:['period'],sma:['period'],rsi:['period'],atr:['period'],aroon:['period'],ichimoku:['fast','slow','period','displacement'],macd:['fast','slow','signal'],bollinger:['period','multiplier']};
const outputs={ema:['value'],sma:['value'],rsi:['value'],atr:['value'],aroon:['up','down','oscillator'],ichimoku:['tenkan','kijun','span_a','span_b'],macd:['line','signal','histogram'],bollinger:['middle','upper','lower']};
function indicatorDefaults(type,id){if(type==='ichimoku')return{id,type,fast:9,slow:26,period:52,displacement:26};if(type==='macd')return{id,type,fast:12,slow:26,signal:9};if(type==='bollinger')return{id,type,period:20,multiplier:2};return{id,type,period:14};}
function fields(){return['open','high','low','close','volume',...draft.indicators.flatMap(i=>(outputs[i.type]||[]).map(o=>i.id+'.'+o)),...($('llm-enabled').checked?['enter_long','exit_long','enter_short','exit_short'].map(k=>'llm.'+k):[])];}
function defaultRule(){return{left:{field:'close'},op:'gt',right:{value:0}};}
function element(tag,text,cls){const e=document.createElement(tag);if(text!==undefined)e.textContent=text;if(cls)e.className=cls;return e;}
function action(text,fn){const b=element('button',text);b.type='button';b.onclick=fn;return b;}
function ruleNode(rule,replace,remove,scope){
 const wrap=element('div',undefined,'rule'),line=element('div',undefined,'rule-line');wrap.append(line);
 const kind=rule.all?'all':rule.any?'any':rule.not?'not':rule.position?'position':rule.signal?'signal':'condition';
 const type=element('select');type.setAttribute('aria-label','Тип условия');
 const kinds=[['condition','Условие'],['position','Состояние позиции'],['all','ALL · И'],['any','ANY · ИЛИ'],['not','NOT · НЕ']];
 if(scope?.startsWith('exit_')||kind==='signal')kinds.splice(2,0,['signal','Сигнал входа']);
 selectOptions(type,kinds);type.value=kind;
 type.onchange=()=>{let next;switch(type.value){case 'condition':next=defaultRule();break;case 'position':next={position:'none'};break;case 'signal':next={signal:scope==='exit_long'?'enter_short':'enter_long'};break;case 'not':next={not:defaultRule()};break;default:next={[type.value]:[defaultRule()]};}replace(next);renderBuilder();};line.append(type);
 if(kind==='position'||kind==='signal'){
  const value=element('select');value.setAttribute('aria-label',kind==='position'?'Состояние позиции':'Сигнал входа');
  selectOptions(value,kind==='position'?[['none','NONE · вне позиции'],['long','LONG'],['short','SHORT']]:[['enter_long','Вход LONG'],['enter_short','Вход SHORT']]);
  value.value=rule[kind];value.onchange=()=>{rule[kind]=value.value;syncJSON();};line.append(value);
 }
 if(kind==='condition'){for(const side of ['left','right']){if(side==='right'){const op=element('select');selectOptions(op,catalog.operators.map(x=>[x,x]));op.value=rule.op;op.onchange=()=>{rule.op=op.value;syncJSON();};line.append(op);}const select=element('select');selectOptions(select,[['#','Число'],...fields().map(f=>[f,f])]);select.value=rule[side]?.field||'#';line.append(select);const val=element('input');val.type='number';val.step='any';val.value=rule[side]?.value??0;val.hidden=select.value!=='#';line.append(val);select.onchange=()=>{rule[side]=select.value==='#'?{value:+val.value}:{field:select.value};val.hidden=select.value!=='#';syncJSON();};val.oninput=()=>{rule[side]={value:+val.value};syncJSON();};}}
 if(remove)line.append(action('×',()=>{remove();renderBuilder();}));
 if(kind==='all'||kind==='any'){const children=element('div',undefined,'rule-children');rule[kind].forEach((child,i)=>children.append(ruleNode(child,v=>rule[kind][i]=v,()=>rule[kind].splice(i,1),scope)));wrap.append(children);const controls=element('div',undefined,'rule-controls');controls.append(action('+ Условие',()=>{rule[kind].push(defaultRule());renderBuilder();}),action('+ Группа',()=>{rule[kind].push({all:[defaultRule()]});renderBuilder();}));wrap.append(controls);}else if(kind==='not'){wrap.append(ruleNode(rule.not,v=>rule.not=v,undefined,scope));}return wrap;
}

function renderBuilder(){$('indicator-builder').replaceChildren();draft.indicators.forEach((i,index)=>{const row=element('div',undefined,'indicator-row'),idLabel=element('label','ID'),id=element('input');id.value=i.id;id.onchange=()=>{i.id=id.value;renderBuilder();};idLabel.append(id);const typeLabel=element('label','Тип'),type=element('select');selectOptions(type,catalog.indicators.map(k=>[k,k.toUpperCase()]));type.value=i.type;type.onchange=()=>{draft.indicators[index]=indicatorDefaults(type.value,i.id);renderBuilder();};typeLabel.append(type);const p=element('div',undefined,'indicator-params');(params[i.type]||[]).forEach(k=>{const l=element('label',k),input=element('input');input.type='number';input.step=k==='multiplier'?'0.1':'1';input.min='1';input.value=i[k];input.oninput=()=>{i[k]=+input.value;syncJSON();};l.append(input);p.append(l);});const remove=action('×',()=>{draft.indicators.splice(index,1);renderBuilder();});remove.className='icon-button';row.append(idLabel,typeLabel,p,remove);$('indicator-builder').append(row);});$('rule-builder').replaceChildren();Object.entries({enter_long:'Вход LONG',exit_long:'Выход LONG',enter_short:'Вход SHORT',exit_short:'Выход SHORT'}).forEach(([key,label])=>{const section=element('div',undefined,'rule-section'),heading=element('div',undefined,'rule-heading');heading.append(element('span',label));heading.append(action(draft.rules[key]?'Отключить':'+ Условие',()=>{if(draft.rules[key])delete draft.rules[key];else draft.rules[key]=defaultRule();renderBuilder();}));section.append(heading);if(draft.rules[key])section.append(ruleNode(draft.rules[key],v=>draft.rules[key]=v,undefined,key));$('rule-builder').append(section);});syncJSON();}
$('new-position').onclick=()=>setDraft(catalog.position_example,'position-trend','Позиция и встречный сигнал · новый черновик');
$('new-d1').onclick=()=>setDraft(catalog.d1_example,'trend-d1-2b','LiquidAI/d1-2B · сохранится новая версия');
$('llm-d1').onclick=()=>{$('llm-provider').value='ollama';$('llm-protocol').value='systemone';$('llm-model').value='d1:2b';if(!$('llm-instructions').value)$('llm-instructions').value=catalog.d1_example.llm.instructions;syncJSON();};
$('new-strategy').onclick=()=>setDraft(catalog.example,'strategy-'+Date.now().toString(36),'Новая стратегия');$('add-indicator').onclick=()=>{draft.indicators.push(indicatorDefaults('ema','indicator_'+(draft.indicators.length+1)));renderBuilder();};
['strategy-name','strategy-description','risk-fraction','risk-stop','risk-take','llm-provider','llm-protocol','llm-model','llm-lookback','llm-instructions'].forEach(id=>$(id).oninput=syncJSON);
$('llm-enabled').onchange=()=>{$('llm-config').hidden=!$('llm-enabled').checked;renderBuilder();};$('apply-json').onclick=safe(()=>{setDraft(JSON.parse($('strategy-json').value),$('strategy-id').value,'JSON применён · ещё не сохранён');});
$('validate-strategy').onclick=safe(async()=>{const r=await post('/api/strategies/validate',readDraft());$('strategy-validation').textContent=`Проверка пройдена. Прогрев: ${r.warmup} свечей. Полей: ${Object.keys(r.fields).length}.`;});
$('strategy-form').onsubmit=safe(async e=>{e.preventDefault();const v=await post('/api/strategies',{id:$('strategy-id').value,strategy:readDraft()});await loadStrategies();$('editing-version').textContent='Сохранена v'+v.version+' · '+v.hash.slice(0,12);notice('Сохранена неизменяемая версия '+v.version);});
function updateDataHint(){
 const source=$('run-source').value,interval=$('run-interval').value;
 const found=symbols('run-symbols').map(symbol=>{const d=datasetInfo.find(d=>d.source===source&&d.symbol===symbol&&d.interval===interval);return d?`${symbol}: ${date(d.from)} — ${date(d.to)} UTC`:`${symbol}: локальной истории пока нет`;});
 const note=source==='demo'?'DEMO — синтетические свечи. Для биржевых данных выберите источник Binance.':catalog?.sources.includes(source)?'Пропуски и свечи прогрева загрузятся автоматически из выбранного источника.':'Для этого источника пропуски нужно импортировать через CSV.';
 $('run-data-hint').textContent=[source+' · '+interval,...found,note].join(' · ');
}
['run-source','run-interval','run-symbols'].forEach(id=>$(id).addEventListener('change',updateDataHint));$('run-symbols').addEventListener('input',updateDataHint);
async function loadData(){const [data,storage]=await Promise.all([api('/api/datasets'),api('/api/storage')]);datasetInfo=data;updateDataHint();$('storage-shards').innerHTML=storage.shards.map(s=>`<div class="shard-card"><strong>${esc(s.symbol)}</strong><code>candles/${esc(s.file)}</code><span>${s.candles.toLocaleString('ru-RU')} свечей · наборов: ${s.datasets} · ${num(s.bytes/1024,0)} КБ</span></div>`).join('')||'<p class="muted">Файлы появятся при первом импорте.</p>';const sources=[...new Set(['demo',...catalog.sources,...data.map(d=>d.source)])];selectOptions($('run-source'),sources.map(s=>[s,s==='demo'?'DEMO · синтетические данные':s]));$('datasets').innerHTML=data.map((d,i)=>`<tr><td>${esc(d.source)}</td><td>${esc(d.symbol)}</td><td>${esc(d.interval)}</td><td>${d.count}</td><td>${date(d.from)} — ${date(d.to)}</td><td><button data-index="${i}" class="subtle">↓ CSV</button></td></tr>`).join('');$('datasets').querySelectorAll('button').forEach(b=>b.onclick=safe(async()=>{const d=data[+b.dataset.index];const blob=await api('/api/candles?'+new URLSearchParams({source:d.source,symbol:d.symbol,interval:d.interval,format:'csv'}),{blob:true});download(`${d.source}-${d.symbol}-${d.interval}.csv`,blob);}));}
$('refresh-data').onclick=safe(loadData);$('import-form').onsubmit=safe(async e=>{e.preventDefault();const button=e.target.querySelector('button[type="submit"]');button.disabled=true;$('import-status').textContent='Загрузка и проверка свечей…';try{const result=await post('/api/import',{source:$('import-source').value,symbols:symbols('import-symbols'),interval:$('import-interval').value,from:utc('import-from'),to:utc('import-to')});notice(`Загружено наборов: ${result.length}`);await loadData();}finally{button.disabled=false;$('import-status').textContent='';}});
$('load-symbols').onclick=safe(async()=>{const list=await api('/api/symbols?source='+encodeURIComponent($('import-source').value));$('symbol-options').replaceChildren(...list.map(v=>{const o=document.createElement('option');o.value=v;return o;}));notice(`Доступно пар: ${list.length}. Начните вводить символ.`);});
$('csv-form').onsubmit=safe(async e=>{e.preventDefault();const file=$('csv-file').files[0];if(!file)return;const q=new URLSearchParams({source:$('csv-source').value,symbol:$('csv-symbol').value.toUpperCase(),interval:$('csv-interval').value});await api('/api/import/csv?'+q,{method:'POST',headers:{'Content-Type':'text/csv'},body:file});await loadData();notice('CSV импортирован');});
async function loadSessions(){const list=await api('/api/sessions');$('session-list').innerHTML=list.map(s=>`<div class="panel session-card"><div class="panel-heading"><h2>${esc(s.strategy.strategy.name)} · v${s.strategy.version} <span class="badge">${esc(s.request.mode.toUpperCase())} / ${esc(s.status)}</span></h2><span>${esc(s.id)}</span></div><p class="muted">${esc(s.request.symbols.join(', '))} · ${esc(s.request.interval)} · ${esc(s.error||'')}</p><pre>${esc(JSON.stringify({positions:s.positions,balances:s.balances,pending:s.pending},null,2))}</pre><div class="inline">${['pause','resume','reconcile','close','detail'].map((action,i)=>`<button data-id="${esc(s.id)}" data-action="${action}">${['Пауза','Продолжить','Сверить ордер','Закрыть позиции','Журнал'][i]}</button>`).join('')}</div></div>`).join('')||'<div class="panel muted">Сессий пока нет. Создайте paper-сессию на текущих рыночных данных.</div>';$('session-list').querySelectorAll('button').forEach(b=>b.onclick=safe(async()=>{if(b.dataset.action==='detail'){showDetail('Журнал сессии',await api('/api/sessions/'+b.dataset.id));return;}if(b.dataset.action==='close'&&!confirm('Закрыть все позиции этой сессии? В live будут отправлены реальные reduce-only ордера.'))return;await post('/api/sessions/'+b.dataset.id+'/'+b.dataset.action);await loadSessions();}));}
$('session-form').onsubmit=safe(async e=>{e.preventDefault();const mode=$('session-mode').value;if(mode==='live'&&!confirm('Запустить реальную торговлю выбранной стратегией и парами? Будут отправляться market-ордера с указанным лимитом.'))return;const[strategy_id,version]=$('session-strategy').value.split(':');await post('/api/sessions',{strategy_id,version:+version,exchange:$('session-exchange').value,mode,symbols:symbols('session-symbols'),interval:$('session-interval').value,capital:+$('session-capital').value,fee_bps:+$('session-fee').value,slippage_bps:+$('session-slippage').value,max_order_notional:+$('session-max').value,allow_llm:$('session-llm').checked,max_llm_calls:+$('session-budget').value});await loadSessions();notice('Сессия создана');});

function settingsCard(v){
 const p=v.provider, root=element('form',undefined,'panel connection-card');root.id='settings-'+p;
 root.innerHTML=`<div class="panel-heading"><h2>${p==='openai'?'OpenAI-compatible':'Ollama'}</h2><span class="badge">${v.source==='saved'?'СОХРАНЕНО':'ИЗ ОКРУЖЕНИЯ'}</span></div>
 <label>Базовый URL<input name="base_url" type="url" required value="${esc(v.base_url)}" spellcheck="false"></label>
 <p class="field-help">${p==='openai'?'Адрес API с /v1, без /chat/completions.':'Адрес Ollama, без /api/chat. Для Docker используйте адрес хоста.'}</p>
 <label>API-ключ<input name="api_key" type="password" autocomplete="new-password" placeholder="${v.api_key_configured?'Ключ настроен · оставьте пустым, чтобы сохранить':'Не задан · введите при необходимости'}"></label>
 <label class="check secret-clear"><input name="clear_api_key" type="checkbox"> Удалить сохранённый ключ</label>
 <div class="form-grid two"><label>Модель по умолчанию<input name="default_model" value="${esc(v.default_model)}" placeholder="Имя модели" list="models-${p}" maxlength="200"><datalist id="models-${p}"></datalist></label><label>Таймаут, секунд<input name="timeout_seconds" type="number" min="1" max="300" required value="${v.timeout_seconds}"></label></div>
 ${p==='openai'?`<label class="check json-mode"><input name="json_mode" type="checkbox" ${v.json_mode?'checked':''}> JSON mode вместо JSON Schema</label><p class="field-help">Для совместимых API без поддержки строгой схемы.</p>`:'<p class="field-help">Chat или System One · протокол выбирается в стратегии. Для LiquidAI/d1-2B укажите d1:2b.</p>'}
 <div class="connection-status muted" role="status">Подключение ещё не проверено.</div>
 <div class="form-footer"><button type="button" data-check>Проверить</button><button type="submit" class="primary">Сохранить</button></div>`;
 const field=n=>root.elements.namedItem(n),status=root.querySelector('.connection-status');
 const patch=()=>{const v={base_url:field('base_url').value,default_model:field('default_model').value,timeout_seconds:+field('timeout_seconds').value,clear_api_key:field('clear_api_key').checked};if(field('api_key').value.trim())v.api_key=field('api_key').value;if(p==='openai')v.json_mode=field('json_mode').checked;return v;};
 const busy=on=>root.querySelectorAll('button,input').forEach(e=>e.disabled=on);
 root.querySelector('[data-check]').onclick=async()=>{if(!root.reportValidity())return;const draft=patch();busy(true);status.className='connection-status muted';status.textContent='Проверяем подключение…';try{const r=await post('/api/settings/llm/'+p+'/check',draft);const list=root.querySelector('datalist');list.replaceChildren(...r.models.map(m=>{const o=element('option');o.value=m;return o;}));status.className='connection-status green';status.textContent=`Соединение установлено · ${r.latency_ms} мс · моделей: ${r.models.length}. `+(draft.default_model?(r.default_model_available?'Выбранная модель доступна.':'Выбранная модель отсутствует в списке.'):'Выберите модель из списка.');}catch(e){status.className='connection-status red';status.textContent=e.message;}finally{busy(false);}};
 root.onsubmit=async e=>{e.preventDefault();const draft=patch();busy(true);try{const saved=await post('/api/settings/llm/'+p,draft);connectionSettings=connectionSettings.map(v=>v.provider===p?saved:v);root.replaceWith(settingsCard(saved));notice('Настройки '+(p==='openai'?'OpenAI-compatible':'Ollama')+' сохранены');}catch(e){status.className='connection-status red';status.textContent=e.message;}finally{busy(false);}};
 return root;
}
async function loadSettings(){connectionSettings=await api('/api/settings/llm');$('connection-settings').replaceChildren(...connectionSettings.map(settingsCard));}
$('llm-default').onclick=()=>{const v=connectionSettings.find(v=>v.provider===$('llm-provider').value);if(!v?.default_model){notice('Сначала укажите модель по умолчанию во вкладке «Настройки».',true);return;}$('llm-model').value=v.default_model;if(v.default_model==='d1:2b'||v.default_model.startsWith('d1:2b-')||v.default_model==='LiquidAI/d1-2B')$('llm-protocol').value='systemone';syncJSON();};

async function init(){catalog=await api('/api/catalog');$('connection').textContent='Сервис подключён';$('mode-badge').textContent=catalog.live_enabled?'LIVE ДОСТУПЕН':'PAPER / RESEARCH';$('session-mode').querySelector('[value="live"]').disabled=!catalog.live_enabled;document.querySelectorAll('.interval').forEach(s=>{selectOptions(s,catalog.intervals.map(x=>[x,x]));s.value='1h';});$('mcp-config').textContent=JSON.stringify({mcpServers:{arcam:{url:location.origin+'/mcp',headers:{Authorization:'Bearer <ARCAM_API_TOKEN, если задан>'}}}},null,2);await Promise.all([loadStrategies(),loadRuns(),loadData(),loadSettings()]);if(!draft)setDraft(catalog.example,'trend-demo','На основе демостратегии');if(runs.length)await selectRun(runs[0].id);else drawCharts();}
setInterval(()=>{if($('tab-sessions').classList.contains('active'))safe(loadSessions)();},10000);
safe(init)();
