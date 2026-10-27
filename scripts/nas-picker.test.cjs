const {test}=require('node:test');
const assert=require('node:assert/strict');
const {matches,DirectoryCache}=require('../server/nasui/web/picker.js');
test('fuzzy names support Chinese, separated characters, fullwidth and case',()=>{
 for(const [name,query]of [['电视剧第一季','电剧季'],['File-236.MP4','f236mp4'],['ＶＩＤＥＯ ２０２６','video 26'],['🎬电影A','🎬a'],['summer vacation 2026','2026 vac']])assert.equal(matches(name,query),true,query);
 assert.equal(matches('报告.pdf','pdf 报'),true);assert.equal(matches('任何名称','  '),true);
 assert.equal(matches('abc','ca'),false);assert.equal(matches('a-b','a.b'),false);assert.equal(matches('电影','音乐'),false);
});
test('cache separates mounts and paths and evicts least recently used directories',()=>{
 const cache=new DirectoryCache(2,10),value=n=>({items:Array(n).fill({name:'fixture'})});
 cache.put('/a','/a/f',value(1));cache.put('/b','/b/f',value(2));assert.equal(cache.get('/c','/a/f'),undefined);
 cache.get('/a','/a/f');cache.put('/a','/a/g',value(3));assert.equal(cache.get('/b','/b/f'),undefined);assert.equal(cache.count,4);
 cache.delete('/a','/a/f');assert.equal(cache.count,3);cache.clear();assert.equal(cache.count,0);assert.equal(cache.entries.size,0);
});
test('cache entry budget includes replacement and refuses oversized snapshots',()=>{
 const cache=new DirectoryCache(5,4),value=n=>({items:Array(n).fill({name:'fixture'})});
 cache.put('/a','/a/f',value(3));cache.put('/a','/a/g',value(2));assert.equal(cache.get('/a','/a/f'),undefined);
 cache.put('/a','/a/g',value(1));assert.equal(cache.count,1);
 cache.put('/a','/a/g',value(5));assert.equal(cache.count,0);assert.equal(cache.get('/a','/a/g'),undefined);
});
