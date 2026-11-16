(function(){
if(typeof echarts==="undefined")return;
var MAP_ASSET="world.geo.json";
// The pie divs sit in the grid, below this inline script. Run once the
// document is parsed so every chart element exists. When already loaded
// (or in the test harness) run immediately.
function boot(m){
var de=document.getElementById("rw-chart-data");
if(!de)return;
var d=JSON.parse(de.textContent);
function hb(v){v=+v||0;var u=["B","KB","MB","GB","TB","PB"],i=0,x=v;while(x>=1024&&i<u.length-1){x/=1024;i++}return (i?x.toFixed(1):x)+" "+u[i]}
function grp(v){return String(v).replace(/\B(?=(\d{3})+(?!\d))/g,",")}
var charts=[];
function mk(id,option){var el=document.getElementById(id);if(!el)return;option.aria={enabled:true,label:{data:{maxCount:24}}};option.aria={enabled:true,label:{data:{maxCount:24}}};
 // On hovering a line point, emphasize the hovered series in the
 // all-series tooltip popup (ECharts has no built-in for this).
 var hot=null;
 if(option.tooltip&&option.tooltip.trigger==="axis"){
  var fmt=option.tooltip.valueFormatter;
  option.tooltip.formatter=function(ps){
   if(!ps||!ps.length)return "";
   var h=document.createElement("div"),title=document.createElement("div"),i,p;
   title.textContent=ps[0].name;h.appendChild(title);
   for(i=0;i<ps.length;i++){p=ps[i];
    var row=document.createElement("div"),label=document.createElement("span"),value=document.createElement("span");
    row.innerHTML=p.marker;
    label.textContent=p.seriesName;label.style.fontWeight=p.seriesName===hot?"bold":"";
    value.textContent=fmt?fmt(p.value):grp(p.value);value.style.cssText="float:right;margin-left:20px;text-align:right";
    row.appendChild(label);row.appendChild(value);h.appendChild(row)}
   return h;
  };
 }
 var c=echarts.init(el,null,{renderer:"canvas"});c.setOption(option);charts.push(c);
 if(option.tooltip&&option.tooltip.trigger==="axis"){
  c.on("mouseover",function(p){hot=p.seriesName});
  c.on("mouseout",function(p){hot=null});
 }}
if(d.hasDays){
 var axis={type:"category",data:d.labels,axisLabel:{rotate:d.labels.length>14?45:0,fontSize:11}};
 mk("rw-chart-activity",{tooltip:{trigger:"axis",confine:true},legend:{top:0},grid:{left:60,right:20,top:40,bottom:80},xAxis:axis,yAxis:{type:"value"},series:[
  {name:"Requests",type:"line",data:d.requests},
  {name:"Visitors",type:"line",data:d.visitors},
  {name:"Files",type:"line",data:d.files},
  {name:"Pages",type:"line",data:d.pages}]});
 mk("rw-chart-traffic",{tooltip:{trigger:"axis",confine:true,valueFormatter:function(v){return hb(v)}},grid:{left:90,right:20,top:20,bottom:80},xAxis:{type:"category",data:d.labels,axisLabel:{rotate:d.labels.length>14?45:0,fontSize:11}},yAxis:{type:"value",axisLabel:{formatter:function(v){return hb(v)}}},series:[{name:"Traffic",type:"line",data:d.bytes}]});
 mk("rw-chart-visits",{tooltip:{trigger:"axis",confine:true},grid:{left:60,right:20,top:20,bottom:80},xAxis:{type:"category",data:d.labels,axisLabel:{rotate:d.labels.length>14?45:0,fontSize:11}},yAxis:{type:"value"},series:[{name:"Visits",type:"line",data:d.visits}]});
  // One line per virtual host; host names fall through to the palette in
  // restyle() because they are not fixed series names.
  if(d.vhosts&&d.vhosts.length){
   var va={type:"category",data:d.labels,axisLabel:{rotate:d.labels.length>14?45:0,fontSize:11}};
   mk("rw-chart-vhost-requests",{tooltip:{trigger:"axis",confine:true},legend:{top:0},grid:{left:60,right:20,top:40,bottom:80},xAxis:va,yAxis:{type:"value"},series:d.vhosts.map(function(h){return{name:h.name,type:"line",data:h.requests}})});
   mk("rw-chart-vhost-traffic",{tooltip:{trigger:"axis",confine:true,valueFormatter:function(v){return hb(v)}},legend:{top:0},grid:{left:90,right:20,top:40,bottom:80},xAxis:{type:"category",data:d.labels,axisLabel:{rotate:d.labels.length>14?45:0,fontSize:11}},yAxis:{type:"value",axisLabel:{formatter:function(v){return hb(v)}}},series:d.vhosts.map(function(h){return{name:h.name,type:"line",data:h.bytes}})});
  }
}
if(d.hasHours){
 mk("rw-chart-hours",{tooltip:{trigger:"axis",confine:true},grid:{left:60,right:20,top:20,bottom:30},xAxis:{type:"category",data:d.hourLabels},yAxis:{type:"value"},series:[{name:"Requests",type:"line",data:d.hours}]});
}
if(d.map&&d.map.length){
 echarts.registerMap("rw-world",m);
 var max=0;for(var i=0;i<d.map.length;i++){if(d.map[i].value>max)max=d.map[i].value}
 mk("rw-chart-map",{tooltip:{trigger:"item",confine:true,formatter:function(p){return p.name+(Number.isFinite(p.value)?": "+p.value+" visitors":": no data")}},
 visualMap:{min:0,max:max,calculable:true,left:10,bottom:10,text:["high","low"],inRange:{color:["#cfe3f7","#58a6ff","#0969da","#8250df"]}},
 series:[{type:"map",map:"rw-world",roam:true,data:d.map,emphasis:{label:{show:false}},select:{label:{show:false}}}]},null);
}
// Pies sit beside their own tables (the tables act as the legend), so no legend.
if(d.os&&d.os.length){
  mk("rw-chart-os",{tooltip:{trigger:"item"},color:["#0969da","#8250df","#00857d","#b08800","#bc4c00","#b23fa7","#757575","#57606a"],series:[{type:"pie",radius:["0%","80%"],data:d.os}]});
}
// Fixed palette keyed to the HTTP range, so 2xx..5xx always mean the same colour.
if(d.statuses&&d.statuses.length){
  var sc={"2xx":"#2da44e","3xx":"#0969da","4xx":"#bc4c00","5xx":"#d1243f","Other":"#8b949e"};
  mk("rw-chart-status",{tooltip:{trigger:"item"},series:[{type:"pie",radius:["0%","80%"],data:d.statuses.map(function(p){return {name:p.name,value:p.value,itemStyle:{color:sc[p.name]||"#8b949e"}}})}]});
}
window.addEventListener("resize",function(){charts.forEach(function(c){c.resize()})});
function restyle(){
 var css=getComputedStyle(document.documentElement),fg=css.getPropertyValue("--fg").trim(),border=css.getPropertyValue("--border").trim(),accent=css.getPropertyValue("--accent").trim(),accent2=css.getPropertyValue("--accent2").trim();
 charts.forEach(function(c){
  var o=c.getOption(),s={color:[accent,accent2,"#00857d","#b08800"],textStyle:{color:fg}},colors={Requests:accent,Visitors:accent2,Files:"#00857d",Pages:"#b08800",Traffic:"#b23fa7",Visits:"#757575"},palette=[accent,accent2,"#00857d","#b08800","#bc4c00","#b23fa7","#757575"];
  s.series=o.series.map(function(series,i){var color=colors[series.name]||palette[i%palette.length];return series.type==="line"?{lineStyle:{color:color},itemStyle:{color:color}}:{}});
  if(o.xAxis&&o.xAxis.length){s.xAxis={axisLabel:{color:fg},axisLine:{lineStyle:{color:border}}};s.yAxis={axisLabel:{color:fg},splitLine:{lineStyle:{color:border}}}}
  if(o.legend&&o.legend.length)s.legend={textStyle:{color:fg}};
  if(o.visualMap&&o.visualMap.length){s.visualMap={textStyle:{color:fg},inRange:{color:[css.getPropertyValue("--card").trim(),accent]}};s.series=[{itemStyle:{areaColor:css.getPropertyValue("--card").trim(),borderColor:css.getPropertyValue("--muted").trim()}}]}
  c.setOption(s);
 });
}
restyle();
new MutationObserver(restyle).observe(document.documentElement,{attributes:true,attributeFilter:["data-palette","data-theme"]});
if(window.matchMedia)window.matchMedia("(prefers-color-scheme:dark)").addEventListener("change",restyle);
}
function init(){
 // The world map is the big payload. Inline: it is embedded in a script tag.
 // External: the page carries no geometry, so it is fetched once from the
 // shared file written beside the index.
 var me=document.getElementById("rw-map-data");
 if(me){boot(JSON.parse(me.textContent));return}
 if(typeof XMLHttpRequest==="undefined")return;
 var x=new XMLHttpRequest();
 x.open("GET",MAP_ASSET);x.responseType="text";
 x.onload=function(){if(x.status>=200&&x.status<300){try{boot(JSON.parse(x.responseText))}catch(e){}}};
 x.send();
}
if(document.readyState==="loading")document.addEventListener("DOMContentLoaded",init);else init();
})();
