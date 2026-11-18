'use strict';

let sessionHistory=null,sessionPair=null,selectedSessionID='',sessionHistoryEpoch=0;
let sessionDecisionPage=0,sessionEventPage=0,sessionView={start:0,count:0},sessionsRefreshing=false;
const sessionStatus=status=>({running:'Работает',paused:'На паузе',stopped:'Остановлена'}[status]||status);
const sessionTime=value=>new Date(value).toISOString().replace('T',' ').slice(0,19);

async function loadSessions(){
 const list=(await api('/api/sessions')).sort((a,b)=>Date.parse(b.created_at)-Date.parse(a.created_at));
 selectOptions($('session-history-select'),list.map(s=>[s.id,`${date(s.created_at)} · ${s.strategy.strategy.name} · ${s.request.mode.toUpperCase()} · ${sessionStatus(s.status)}`]));
 if(selectedSessionID)$('session-history-select').value=selectedSessionID;
 $('session-list').innerHTML=list.map(s=>`<div class="panel session-card"><div class="panel-heading"><h2>${esc(s.strategy.strategy.name)} · v${s.strategy.version} <span class="badge">${esc(s.request.mode.toUpperCase())} / ${esc(sessionStatus(s.status))}</span></h2><span>${date(s.created_at)} UTC</span></div><p class="muted">${esc(s.request.symbols.join(', '))} · ${esc(s.request.interval)} · ${esc(s.id)}</p>${s.error?`<p class="red">${esc(s.error)}</p>`:''}<div class="session-balances">${s.request.symbols.map(symbol=>{const p=s.positions?.[symbol];return `<span><strong>${esc(symbol)}</strong> · баланс ${num(s.balances?.[symbol])} · ${p?.side?esc(p.side.toUpperCase())+' '+num(p.quantity,6)+' @ '+num(p.entry):'Нет позиции'}</span>`;}).join('')}</div>${s.pending?'<p class="gold">Ордер ожидает сверки</p>':''}<div class="inline session-actions"><button class="primary" data-id="${esc(s.id)}" data-action="history">История ↗</button>${['pause','resume','reconcile','close','detail'].map((action,i)=>`<button data-id="${esc(s.id)}" data-action="${action}">${['Пауза','Продолжить','Сверить ордер','Закрыть позиции','JSON'][i]}</button>`).join('')}</div></div>`).join('')||'<div class="panel muted">Сессий пока нет. Создайте paper-сессию на текущих рыночных данных.</div>';
 $('session-list').querySelectorAll('button').forEach(b=>b.onclick=safe(async()=>{
  const id=b.dataset.id,action=b.dataset.action;
  if(action==='history'){await selectSession(id);return;}
  if(action==='detail'){showDetail('Журнал сессии · JSON',await api('/api/sessions/'+encodeURIComponent(id)));return;}
  if(action==='close'&&!confirm('Закрыть все позиции этой сессии? В live будут отправлены реальные reduce-only ордера.'))return;
  b.disabled=true;
  try{await post('/api/sessions/'+encodeURIComponent(id)+'/'+action);await refreshSessions();}finally{b.disabled=false;}
 }));
}

async function refreshSessions(){
 if(sessionsRefreshing)return;
 sessionsRefreshing=true;
 try{await loadSessions();if(selectedSessionID)await loadSessionHistory(selectedSessionID,true);}
 finally{sessionsRefreshing=false;}
}

async function selectSession(id){
 if(!id)return;
 selectedSessionID=id;sessionHistory=null;sessionPair=null;
 sessionDecisionPage=0;sessionEventPage=0;
 $('session-follow').checked=true;
 $('session-overview').hidden=true;$('session-history').hidden=false;
 $('session-history-content').hidden=true;$('session-download').disabled=true;
 $('session-history-title').textContent='Загрузка истории…';$('session-history-status').textContent='';
 $('session-history-select').value=id;
 try{await loadSessionHistory(id,false);}
 catch(e){if(selectedSessionID===id)$('session-history-title').textContent='Не удалось загрузить историю. Нажмите «Обновить».';throw e;}
}

async function loadSessionHistory(id,preserve){
 const epoch=++sessionHistoryEpoch;
 const history=await api('/api/sessions/'+encodeURIComponent(id)+'/history');
 if(selectedSessionID!==id||epoch!==sessionHistoryEpoch)return;
 sessionHistory=history;
 const s=history.session;
 $('session-history-title').textContent=`${s.strategy.strategy.name} · v${s.strategy.version}`;
 $('session-history-status').textContent=`${s.request.mode.toUpperCase()} / ${sessionStatus(s.status)}`;
 $('session-history-meta').textContent=`${s.id} · ${s.request.exchange} · ${s.request.interval} · создана ${date(s.created_at)} UTC`;
 $('session-history-error').textContent=s.error||'';$('session-history-error').hidden=!s.error;
 $('session-history-updated').textContent='Обновлено '+sessionTime(new Date())+' UTC';
 $('session-history-content').hidden=false;$('session-download').disabled=false;
 selectOptions($('session-chart-pair'),history.pairs.map(p=>[p.symbol,p.symbol]));
 renderSessionPair(preserve);
}

function renderSessionPair(preserve=false){
 if(!sessionHistory)return;
 const oldSymbol=sessionPair?.symbol,oldTime=sessionPair?.candles[+$('session-chart-offset').value]?.time;
 sessionPair=sessionHistory.pairs.find(p=>p.symbol===$('session-chart-pair').value)||null;
 const p=sessionPair,s=sessionHistory.session;
 if(!p)return;
 if(!preserve||oldSymbol!==p.symbol){sessionDecisionPage=0;sessionEventPage=0;}
 const step=intervalMilliseconds(s.request.interval),byTime=new Map(p.decisions.map(d=>[Date.parse(d.time),d]));
 p.chartDecisions=p.candles.map(c=>byTime.get(Date.parse(c.time)+step));
 p.fills=p.events.filter(e=>e.fill).map(e=>e.fill);
 let cursor=0,value;
 p.equity=p.candles.map(c=>{
  const at=Date.parse(c.time)+step;
  while(cursor<p.balance.length&&Date.parse(p.balance[cursor].time)<=at)value=p.balance[cursor++].value;
  return {time:c.time,value};
 });
 const strategy=s.strategy.strategy;
 const fields=[...(strategy.indicators||[]).flatMap(i=>(outputs[i.type]||[]).map(o=>i.id+'.'+o)),...(strategy.llm?['enter_long','exit_long','enter_short','exit_short'].map(k=>'llm.'+k):[])];
 selectOptions($('session-chart-feature'),[['','Без индикатора'],...fields.map(k=>[k,k])]);
 const max=Math.max(0,p.candles.length-+$('session-chart-window').value);
 $('session-chart-offset').max=max;
 const anchored=oldTime?p.candles.findIndex(c=>Date.parse(c.time)>=Date.parse(oldTime)):0;
 $('session-chart-offset').value=$('session-follow').checked||!preserve||oldSymbol!==p.symbol?max:Math.min(max,Math.max(0,anchored));
 $('session-chart-caption').textContent=`${p.symbol} · ${s.request.interval} · ${p.candles.length} свечей · ${p.decisions.length} решений`;
 const pnl=p.trades.reduce((sum,t)=>sum+t.pnl,0),wins=p.trades.filter(t=>t.pnl>0).length;
 const cards=[['БАЛАНС ПАРЫ',num(s.balances[p.symbol]),''],['РЕШЕНИЯ',num(p.decisions.length,0),''],['ИСПОЛНЕНИЯ',num(p.fills.length,0),''],['СДЕЛКИ / WIN RATE',p.trades.length+' / '+(p.trades.length?num(100*wins/p.trades.length,1)+'%':'—'),''],['PnL ЗАКРЫТЫХ СДЕЛОК',num(pnl),pnl>=0?'green':'red']];
 $('session-metrics').innerHTML=cards.map(([label,value,color])=>`<div class="metric"><small>${label}</small><strong class="${color}">${value}</strong></div>`).join('');
 const position=s.positions[p.symbol];
 $('session-position').textContent=position?.side?`Открыта ${position.side.toUpperCase()} · вход ${num(position.entry)} · количество ${num(position.quantity,6)}${position.stop_price?' · Stop '+num(position.stop_price):''}${position.target_price?' · Target '+num(position.target_price):''}`:'Позиция не открыта';
 $('session-pending').hidden=!s.pending;
 $('session-pending').textContent=s.pending?`Ожидает сверки: ${s.pending.order.symbol} · ${s.pending.action} · ${s.pending.order.quantity}`:'';
 $('session-trades').innerHTML=tradeHistoryRows(p.trades.slice().reverse());
 drawSessionCharts();renderSessionDecisions();renderSessionEvents();
}

function drawSessionCharts(){
 if(!$('tab-sessions').classList.contains('active')||$('session-history-content').hidden||!sessionHistory)return;
 sessionView=drawHistoryCharts('session-',sessionPair,sessionHistory.session.request.interval,'УЧЁТНЫЙ БАЛАНС · БЕЗ ОТКРЫТОГО PnL');
}
function renderSessionDecisions(){sessionDecisionPage=renderDecisionHistory('session-',sessionPair?.decisions||[],sessionDecisionPage,$('session-follow').checked);}
function renderSessionEvents(){
 const list=sessionPair?.events||[],last=Math.max(0,Math.ceil(list.length/30)-1);
 sessionEventPage=$('session-follow').checked?last:Math.min(sessionEventPage,last);
 const page=list.slice(sessionEventPage*30,(sessionEventPage+1)*30);
 $('session-event-page').textContent=`${list.length?sessionEventPage+1:0} / ${Math.ceil(list.length/30)} · ${list.length}`;
 $('session-event-prev').disabled=sessionEventPage===0;$('session-event-next').disabled=sessionEventPage===last;
 $('session-events').innerHTML=page.map((e,i)=>`<tr><td>${sessionTime(e.time)}</td><td><code>${esc(e.fill?.action||e.execution?.action||'—')}</code></td><td>${esc(e.execution?.status||(e.fill?'filled':e.receipt?.status)||'—')}</td><td>${e.fill?num(e.fill.price):'—'}</td><td>${e.fill?num(e.fill.quantity,6):'—'}</td><td class="reason">${esc(e.execution?.reason||e.fill?.reason||e.receipt?.status||'')}</td><td><button class="subtle" data-event="${i}">Детали ↗</button></td></tr>`).join('')||'<tr><td colspan="7" class="muted">Исполнений и пропусков пока нет.</td></tr>';
 $('session-events').querySelectorAll('button').forEach(b=>b.onclick=()=>showDetail('Исполнение · '+sessionPair.symbol,page[+b.dataset.event]));
}

$('session-history-back').onclick=()=>{
 selectedSessionID='';sessionHistoryEpoch++;sessionHistory=null;sessionPair=null;
 $('session-history').hidden=true;$('session-overview').hidden=false;safe(loadSessions)();
};
$('session-history-select').onchange=safe(()=>selectSession($('session-history-select').value));
$('session-history-refresh').onclick=safe(refreshSessions);
$('session-download').onclick=safe(async()=>{const id=selectedSessionID;if(id)download('arcam-session-'+id+'.json',await api('/api/sessions/'+encodeURIComponent(id)));});
$('session-chart-pair').onchange=()=>renderSessionPair();
$('session-chart-feature').onchange=drawSessionCharts;
$('session-chart-window').onchange=()=>renderSessionPair(true);
$('session-chart-offset').oninput=()=>{$('session-follow').checked=false;drawSessionCharts();};
$('session-follow').onchange=()=>renderSessionPair(true);
$('session-decision-filter').onchange=()=>{sessionDecisionPage=0;renderSessionDecisions();};
$('session-prev-page').onclick=()=>{$('session-follow').checked=false;sessionDecisionPage=Math.max(0,sessionDecisionPage-1);renderSessionDecisions();};
$('session-next-page').onclick=()=>{$('session-follow').checked=false;sessionDecisionPage++;renderSessionDecisions();};
$('session-event-prev').onclick=()=>{$('session-follow').checked=false;sessionEventPage=Math.max(0,sessionEventPage-1);renderSessionEvents();};
$('session-event-next').onclick=()=>{$('session-follow').checked=false;sessionEventPage++;renderSessionEvents();};
bindHistoryChart('session-',()=>sessionPair,()=>sessionView);
window.addEventListener('resize',drawSessionCharts);
setInterval(()=>{if($('tab-sessions').classList.contains('active'))safe(refreshSessions)();},10000);
