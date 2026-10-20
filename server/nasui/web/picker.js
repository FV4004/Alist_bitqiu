/* Shared, session-only helpers for the two directory pickers. */
(() => {
  'use strict';
  const normalize = value => String(value || '').normalize('NFKC').toLowerCase();
  function matches(name, query) {
    const text = [...normalize(name)];
    return normalize(query).trim().split(/\s+/u).filter(Boolean).every(term => {
      let pos = 0;
      for (const letter of term) {
        while (pos < text.length && text[pos] !== letter) pos++;
        if (pos === text.length) return false;
        pos++;
      }
      return true;
    });
  }
  class DirectoryCache {
    constructor(limit = 24, maxEntries = 100000) { this.limit = limit; this.maxEntries = maxEntries; this.entries = new Map(); this.count = 0; }
    key(root, path) { return JSON.stringify([root, path]); }
    get(root, path) {
      const key = this.key(root, path), value = this.entries.get(key);
      if (value) { this.entries.delete(key); this.entries.set(key, value); }
      return value;
    }
    delete(root, path) {
      const key = this.key(root, path), old = this.entries.get(key);
      if (old) { this.count -= old.items.length; this.entries.delete(key); }
    }
    put(root, path, value) {
      this.delete(root, path);
      if (value.items.length > this.maxEntries) return;
      this.entries.set(this.key(root, path), value); this.count += value.items.length;
      while (this.entries.size > this.limit || this.count > this.maxEntries) {
        const key = this.entries.keys().next().value;
        this.count -= this.entries.get(key).items.length; this.entries.delete(key);
      }
    }
    clear() { this.entries.clear(); this.count = 0; }
  }
  function sortItems(items,field='modified',direction='desc',foldersFirst=true){
    const value=file=>field==='modified'?Date.parse(file.modified):file.is_dir||file.size_known===false?NaN:Number(file.size);
    return [...items].sort((a,b)=>{
      if(foldersFirst&&!!a.is_dir!==!!b.is_dir)return a.is_dir?-1:1;
      let delta=0;
      if(field==='name')delta=a.name.localeCompare(b.name,'zh-CN',{numeric:true,sensitivity:'base'});
      else{const av=value(a),bv=value(b),ak=Number.isFinite(av)&&!(field==='size'&&av<0),bk=Number.isFinite(bv)&&!(field==='size'&&bv<0);if(ak!==bk)return ak?-1:1;if(ak)delta=av-bv;}
      if(delta)return direction==='desc'?-delta:delta;
      return a.name.localeCompare(b.name,'zh-CN',{numeric:true});
    });
  }
  const api = { matches, DirectoryCache, sortItems };
  if (typeof module !== 'undefined' && module.exports) module.exports = api;
  else window.NASPicker = api;
})();
