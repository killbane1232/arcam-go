'use strict';

function intervalMilliseconds(interval){
 const match=/^(\d+)([mhd])$/.exec(interval||'');
 return match?+match[1]*{m:60000,h:3600000,d:86400000}[match[2]]:3600000;
}

// Shared by backtest results and session history. Decisions are aligned by the
// caller so missing session frames stay empty instead of shifting later signals.
function drawHistoryCharts(prefix,result,interval,balanceLabel='КАПИТАЛ ПАРЫ'){
 const el=id=>$(prefix+id), barsAll=result?.candles||[];
 el('chart-empty').hidden=!!barsAll.length;
 el('chart-tooltip').hidden=true;
 const {ctx,w,h}=canvas(prefix+'price-chart');
 const start=+el('chart-offset').value, count=Math.min(+el('chart-window').value,barsAll.length-start);
 const view={start,count};
 const bars=barsAll.slice(start,start+count);
 if(!bars.length){
  lineChart(prefix+'indicator-chart',[],'Индикатор','#e5bf7b');
  lineChart(prefix+'equity-chart',[],balanceLabel,'#9be1c0');
  return view;
 }
 const decisions=result.chartDecisions||result.decisions||[];
 let lo=Math.min(...bars.map(c=>c.low)),hi=Math.max(...bars.map(c=>c.high));
 const pad=(hi-lo)*.07||1;lo-=pad;hi+=pad;
 grid(ctx,w,h,lo,hi,'OHLC');
 const width=(w-80)/bars.length,x=i=>(i+.5)*width,y=price=>25+(hi-price)/(hi-lo)*(h-50);
 bars.forEach((c,i)=>{
  ctx.strokeStyle=ctx.fillStyle=c.close>=c.open?'#8ed9b8':'#eb8c9f';
  ctx.beginPath();ctx.moveTo(x(i),y(c.high));ctx.lineTo(x(i),y(c.low));ctx.stroke();
  ctx.fillRect(x(i)-Math.max(.8,width*.55)/2,Math.min(y(c.open),y(c.close)),Math.max(.8,width*.55),Math.max(1,Math.abs(y(c.open)-y(c.close))));
  const d=decisions[start+i];
  if(d){
   ctx.fillStyle=d.action==='hold'?'#41536c':d.action.includes('short')?'#f48c9b':'#9be1c0';
   ctx.beginPath();ctx.arc(x(i),h-10,d.action==='hold'?1.2:2.7,0,Math.PI*2);ctx.fill();
  }
 });
 const step=intervalMilliseconds(interval),times=bars.map(c=>Date.parse(c.time));
 for(const f of result.fills||[]){
  const time=Date.parse(f.time);
  // Binary search also handles gaps in a saved candle series.
  let left=0,right=times.length;
  while(left<right){const mid=(left+right)>>1;if(times[mid]<=time)left=mid+1;else right=mid;}
  const i=left-1;
  if(i<0||time>=times[i]+step&&!(result.complete&&i===bars.length-1&&time===times[i]+step))continue;
  const xx=x(i),yy=y(f.price);
  ctx.fillStyle=f.action.startsWith('exit')?'#e5bf7b':f.action.includes('long')?'#9be1c0':'#f48c9b';
  ctx.beginPath();
  if(f.action.startsWith('exit')){ctx.moveTo(xx,yy-6);ctx.lineTo(xx+5,yy);ctx.lineTo(xx,yy+6);ctx.lineTo(xx-5,yy);}
  else{const dir=f.action==='enter_long'?1:-1;ctx.moveTo(xx,yy-dir*7);ctx.lineTo(xx-5,yy+dir*4);ctx.lineTo(xx+5,yy+dir*4);}
  ctx.closePath();ctx.fill();
 }
 ctx.fillStyle='#8e9db2';ctx.fillText(date(bars[0].time),4,h-22);ctx.fillText(date(bars.at(-1).time),Math.max(160,w-210),h-22);
 const feature=el('chart-feature').value;
 lineChart(prefix+'indicator-chart',decisions.slice(start,start+count).map(d=>d?.features?.[feature]),feature||'Выберите индикатор','#e5bf7b');
 lineChart(prefix+'equity-chart',(result.equity||[]).slice(start,start+count).map(e=>e?.value),balanceLabel,'#9be1c0');
 return view;
}

function bindHistoryChart(prefix,getResult,getView){
 const chart=$(prefix+'price-chart'),tooltip=$(prefix+'chart-tooltip');
 function hovered(e){
  const result=getResult(),view=getView();
  if(!result?.candles?.length||!view.count)return {};
  const rect=chart.getBoundingClientRect();
  const i=view.start+Math.min(view.count-1,Math.max(0,Math.floor((e.clientX-rect.left)/(rect.width-80)*view.count)));
  return {c:result.candles[i],d:(result.chartDecisions||result.decisions)[i],symbol:result.symbol};
 }
 chart.onmousemove=e=>{
  const {c,d}=hovered(e);if(!c)return;
  tooltip.textContent=`${date(c.time)} UTC\nO ${num(c.open)}  H ${num(c.high)}  L ${num(c.low)}  C ${num(c.close)}\n${d?d.action+' · '+d.reason+'\nНажмите для дерева условий':'Для этой свечи нет сохранённого решения'}`;
  tooltip.hidden=false;
 };
 chart.onmouseleave=()=>tooltip.hidden=true;
 chart.onclick=e=>{const {d,symbol}=hovered(e);if(d)showDetail('Решение · '+symbol,d);};
}

function renderDecisionHistory(prefix,decisions,page,follow){
 const el=id=>$(prefix+id);
 const list=decisions.filter(d=>el('decision-filter').value==='all'||d.action!=='hold');
 const last=Math.max(0,Math.ceil(list.length/30)-1);
 page=follow?last:Math.min(page,last);
 const rows=list.slice(page*30,(page+1)*30);
 el('page-label').textContent=`${list.length?page+1:0} / ${Math.ceil(list.length/30)} · ${list.length}`;
 el('prev-page').disabled=page===0;el('next-page').disabled=page===last;
 el('decisions').innerHTML=rows.map((d,i)=>`<tr><td>${date(d.time)}</td><td class="${d.action.includes('short')?'red':d.action==='hold'?'':'green'}"><code>${esc(d.action)}</code></td><td>${num(d.features?.close)}</td><td class="reason">${esc(d.llm?.verdict.reason||(d.reason+(d.llm_status==='skipped'?' · LLM не требовалась':'')))}</td><td><button data-index="${i}" class="subtle">Условия ↗</button></td></tr>`).join('')||'<tr><td colspan="5" class="muted">Нет решений для выбранного фильтра.</td></tr>';
 el('decisions').querySelectorAll('button').forEach(b=>b.onclick=()=>showDetail('Дерево условий и LLM',rows[+b.dataset.index]));
 return page;
}

function tradeHistoryRows(trades){
 return trades.map(t=>`<tr><td class="${t.side==='long'?'green':'red'}">${esc(t.side)}</td><td>${date(t.entry_time)}</td><td>${date(t.exit_time)}</td><td>${num(t.entry)} → ${num(t.exit)}</td><td class="${t.pnl>=0?'green':'red'}">${num(t.pnl)}</td><td>${esc(t.reason)}</td></tr>`).join('')||'<tr><td colspan="6" class="muted">Закрытых сделок пока нет.</td></tr>';
}
