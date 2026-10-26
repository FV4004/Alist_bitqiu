const {test} = require('node:test');
const assert = require('node:assert/strict');
const {MB,SizeFilter,parseBound,knownSize} = require('../server/nasui/web/size-filter.js');
const file = (mb, extra={}) => ({name:'fixture',size:Math.round(mb*MB),size_known:true,...extra});
test('80 MB default is inclusive, unlimited above, preserves folders and rejects unknown sizes',()=>{
 const f=new SizeFilter();
 for(const side of ['source','target']){
  assert.equal(f.matches(file(79.999999),side),false);assert.equal(f.matches(file(80),side),true);assert.equal(f.matches(file(20000),side),true);
  assert.equal(f.matches(file(0,{is_dir:true,size_known:false}),side),true);
  for(const unknown of [{size_known:false,size:0},{},{size:-1},{size:null},{size:'90000000'}])assert.equal(f.matches(unknown,side),false);
 }
});
test('blank bounds are independently unlimited; zero remains a real bound',()=>{
 const f=new SizeFilter();f.setBounds('','');assert.equal(f.active('source'),false);assert.equal(f.matches({size_known:false},'source'),true);
 f.setBounds('', '80');assert.equal(f.matches(file(0),'source'),true);assert.equal(f.matches(file(80),'source'),true);assert.equal(f.matches(file(80.000001),'source'),false);
 f.setBounds('0','0');assert.equal(f.matches(file(0),'source'),true);assert.equal(f.matches(file(0.000001),'source'),false);assert.equal(f.matches({size_known:false,size:0},'source'),false);
 f.setBounds('80.5','80.5');assert.equal(f.matches(file(80.5),'source'),true);assert.equal(f.matches(file(80),'source'),false);
 // A fractional-byte boundary must not be rounded into the allowed range.
 f.setBounds('0.000001','');assert.equal(f.matches({size:1},'source'),false);assert.equal(f.matches({size:2},'source'),true);
 f.setBounds('','0.000001');assert.equal(f.matches({size:1},'source'),true);assert.equal(f.matches({size:2},'source'),false);
});
test('invalid and reversed inputs preserve the last applied range',()=>{
 const f=new SizeFilter();f.setBounds('80','500');
 for(const [a,b]of [['501','500'],['-1',''],['text',''],['1e3',''],['Infinity',''],['1.1234567',''],['999999999999999','']]){
  assert.throws(()=>f.setBounds(a,b));assert.equal(f.min,80);assert.equal(f.max,500);
 }
 assert.equal(parseBound('   '),null);assert.equal(parseBound('.5'),.5);assert.equal(knownSize({size:0}),true);
});
test('unchecking one application scope does not disable the other',()=>{
 const f=new SizeFilter();f.scopes.delete('source');assert.equal(f.matches(file(1),'source'),true);assert.equal(f.matches(file(1),'target'),false);
 assert.equal(f.key('source'),null);assert.deepEqual(f.key('target'),[80,null]);
});
test('nonlinear slider values round-trip, expands for manual bounds, and never crosses handles',()=>{
 const f=new SizeFilter();f.setBounds('20000','40000');f.updateDomain([file(100000)]);assert.ok(f.domain>=100000);
 for(const value of [0,80,500,1000,5000,20000,40000,100000])assert.ok(Math.abs(f.fromPosition(f.toPosition(value,'max'),'max')-value)<.011);
 assert.equal(f.fromPosition(1000,'max'),null);assert.equal(f.toPosition(null,'max'),1000);
 f.setBounds('80','500');f.move('min',750);assert.equal(f.min,500);f.move('max',0);assert.equal(f.max,500);f.move('max',1000);assert.equal(f.max,null);
});
