package reqwind

import (
	"os/exec"
	"strings"
	"testing"
)

func TestPaletteKeepsGradientOnMapOnly(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("Node.js is needed to check chart theme updates")
	}
	stub := `
const assert=require("node:assert/strict"),instances=[],callbacks=[];
const data={hasDays:true,labels:["2026-11"],requests:[2],visitors:[1],files:[1],pages:[1],visits:[1],bytes:[42],hasHours:true,hours:Array(24).fill(0),map:[{name:"Germany",value:1}]};
global.document={documentElement:{},getElementById(id){return {id,textContent:JSON.stringify(id==="rw-chart-data"?data:{type:"FeatureCollection",features:[]})}}};
document.createElement=tagName=>({tagName,style:{},children:[],appendChild(child){this.children.push(child)}});
global.window={addEventListener(){},matchMedia(){return {addEventListener(_,fn){callbacks.push(fn)}}}};
global.MutationObserver=class{constructor(fn){callbacks.push(fn)}observe(){}};
global.getComputedStyle=()=>({getPropertyValue:name=>name==="--fg"?"#123456":name==="--accent"?"#654321":"#777777"});
global.echarts={registerMap(){},init(){
 const option={xAxis:[],legend:[],visualMap:[]};
 const chart={getOption:()=>option,resize(){},handlers:[],on(ev,fn){chart.handlers.push({ev,fn})},setOption(update){
  for(const key of Object.keys(update)){
   const value=update[key];
   if(key==="series"&&option.series){option.series=value.map((s,i)=>Object.assign({},option.series[i],s))}
   else option[key]=["xAxis","legend","visualMap"].includes(key)&&!Array.isArray(value)?[value]:value;
  }
 }};
 instances.push(chart);return chart;
}};
`
	check := `
for(let i=0;i<3;i++){
 callbacks.forEach(fn=>fn());
 assert.deepEqual(instances.map(c=>c.getOption().visualMap.length),[0,0,0,0,1]);
 assert.equal(new Set(instances[0].getOption().series.map(s=>s.itemStyle.color)).size,4);
}
const act=instances[0];
const tip=(name,value)=>({name:"2026-11",seriesName:name,marker:"<i></i>",value});
act.handlers.filter(h=>h.ev==="mouseover").forEach(h=>h.fn({seriesName:"Requests"}));
const fmt=act.getOption().tooltip.formatter;
assert.equal(fmt([tip("Requests",2)]).children[1].children[0].style.fontWeight,"bold");
assert.equal(fmt([tip("Requests",1234567)]).children[1].children[1].textContent,"1,234,567");
assert.equal(fmt([tip("Requests",999)]).children[1].children[1].textContent,"999");
act.handlers.filter(h=>h.ev==="mouseout").forEach(h=>h.fn({seriesName:"Requests"}));
assert.equal(fmt([tip("Requests",2)]).children[1].children[0].style.fontWeight,"");
const hostile="<svg/onload=alert(1)>",popup=fmt([{name:hostile,seriesName:hostile,marker:"<i></i>",value:2}]);
assert.equal(popup.children[0].textContent,hostile);
assert.equal(popup.children[1].children[0].textContent,hostile);
assert.equal(popup.children[1].children.length,2);
`
	if out, err := exec.Command(node, "-e", stub+chartInitJS+check).CombinedOutput(); err != nil {
		t.Fatalf("chart theme regression: %v\n%s", err, out)
	}
}

func TestPaletteSurvivesThemeChanges(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("Node.js is needed to check settings events")
	}
	start := strings.Index(reportHTML, "<script>(function(){var a=")
	if start < 0 {
		t.Fatal("settings script missing")
	}
	script := strings.SplitN(reportHTML[start+len("<script>"):], "</script>", 2)[0]
	stub := `
const assert=require("node:assert/strict"),elements={},stored=new Map();
for(const id of ["palette-select","theme-toggle","theme-name"]){
 elements[id]={handlers:{},addEventListener(event,fn){this.handlers[event]=fn},setAttribute(){}};
}
const settings={open:false,contains:()=>false};
global.document={documentElement:{dataset:{palette:"ocean"}},getElementById:id=>elements[id],
 querySelector:sel=>sel===".settings"?settings:null,addEventListener:(ev,fn)=>{global.__listeners=global.__listeners||{};global.__listeners[ev]=fn}};
global.localStorage={getItem:key=>stored.get(key),setItem:(key,value)=>stored.set(key,value),removeItem:key=>stored.delete(key)};
global.window={matchMedia:()=>({matches:false,addEventListener(){}})};
`
	check := `
const select=elements["palette-select"],button=elements["theme-toggle"],root=document.documentElement;
select.value="forest";select.handlers.change();
assert.equal(root.dataset.palette,"forest");
for(let i=0;i<3;i++){
 button.handlers.click();
 assert.equal(root.dataset.palette,"forest");
 assert.equal(select.value,"forest");
}
assert.equal(stored.get("reqwind-palette"),"forest");
document.querySelector(".settings").open=true;
__listeners["click"]({target:{}});
assert.equal(document.querySelector(".settings").open,false);
`
	if out, err := exec.Command(node, "-e", stub+script+check).CombinedOutput(); err != nil {
		t.Fatalf("settings event regression: %v\n%s", err, out)
	}
}
