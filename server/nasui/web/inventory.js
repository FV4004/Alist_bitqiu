/* global window, document, URL, Blob */
(() => {
  "use strict";
  const labels = {
    suspected_missing: "疑似缺失",
    suspected_move: "疑似移动",
    suspected_rename: "疑似改名",
    size_difference: "同路径大小差异",
    content_difference: "内容差异",
    nas_only: "NAS 独有",
    indeterminate: "无法判断",
    content_unverified: "路径和大小一致，内容未校验",
    content_verified: "完整哈希一致",
    directory_present: "同路径目录",
  };
  const decisions = {
    missing: "已确认缺失",
    exists: "已存在并关联候选",
    ignored: "忽略",
    pending: "待处理",
  };
  const decision = (item) => {
    const d = item.detail;
    if (!d || d.stale) return "pending";
    return d.reviews?.at(-1)?.decision || "pending";
  };
  window.NASInventory = {
    labels,
    decision,
    create({ api, state, run, bytes, prepare, headers, base }) {
      const $ = (id) => document.getElementById(id),
        node = (tag, text, cls) => {
          const e = document.createElement(tag);
          if (text !== undefined) e.textContent = text;
          if (cls) e.className = cls;
          return e;
        };
      const root = $("inventory-module");
      let result = null,
        job = null,
        attempt = null,
        tab = "scope",
        selected = new Set(),
        downloadSelected = new Set(),
        limit = 100,
        busy = false,
        complete = true;
      const names = {
        scope: "1 范围设置",
        inventory: "2 双端盘点",
        differences: "3 差异匹配",
        review: "4 人工复核",
        export: "5 报告导出",
        download: "6 补缺下载",
      };
      const tabs = node("div", undefined, "task-tabs");
      tabs.setAttribute("role", "tablist");
      tabs.setAttribute("aria-label", "盘点与复核");
      const content = node("div");
      root.append(tabs, content);
      for (const [key, title] of Object.entries(names)) {
        const b = node("button", title);
        b.type = "button";
        b.setAttribute("role", "tab");
        b.onclick = () => {
          tab = key;
          limit = 100;
          render();
        };
        b.dataset.tab = key;
        tabs.append(b);
      }
      const options = {
        enabled: true,
        nas_only: true,
        cross_directory: false,
        full_hash: false,
      };
      const query = {
        category: "all",
        text: "",
        review: "all",
        directory: "",
        type: "all",
      };
      const form = node("div");
      for (const [key, title] of [
        ["nas_only", "列出 NAS 独有项目"],
        ["cross_directory", "在所选范围内跨目录寻找候选"],
        ["full_hash", "计算 NAS 完整 MD5 / SHA-256（可能耗时）"],
      ]) {
        const label = node("label", undefined, "check"),
          input = node("input");
        input.type = "checkbox";
        input.checked = options[key];
        input.onchange = () => {
          options[key] = input.checked;
        };
        label.append(input, node("span", title));
        form.append(label);
      }
      function filter(items) {
        return items.filter(
          (v) =>
            (query.category === "all" || v.category === query.category) &&
            (!query.text ||
              v.relative.toLowerCase().includes(query.text.toLowerCase())) &&
            (query.review === "all" || decision(v) === query.review) &&
            (!query.directory || v.relative.startsWith(query.directory)) &&
            (query.type === "all" ||
              (query.type === "directory"
                ? v.is_dir
                : !v.is_dir && v.relative.toLowerCase().endsWith(query.type))),
        );
      }
      function button(text, fn) {
        const b = node("button", text, "secondary");
        b.type = "button";
        b.disabled = busy;
        b.onclick = run(fn);
        return b;
      }
      function optionSelect(label, values, value, onchange) {
        const wrap = node("label", label),
          select = node("select");
        for (const [key, title] of Object.entries(values)) {
          const o = node("option", title);
          o.value = key;
          select.append(o);
        }
        select.value = value;
        select.onchange = () => onchange(select.value);
        wrap.append(select);
        return wrap;
      }
      function checkbox(label, checked, onchange) {
        const wrap = node("label", undefined, "check"),
          input = node("input");
        input.type = "checkbox";
        input.checked = checked;
        input.onchange = () => onchange(input.checked);
        wrap.append(input, node("span", label));
        return wrap;
      }
      function objectCard(title, o) {
        const card = node("section", undefined, "inventory-card");
        card.append(node("h4", title));
        if (!o) {
          card.append(node("p", "此路径未盘点到对象"));
          return card;
        }
        const dl = node("dl");
        for (const [key, value] of [
          ["路径", o.path],
          ["对象标识", o.object_id || "未知"],
          ["类型", o.is_dir ? "目录" : "文件"],
          ["大小", o.size_known ? bytes(o.size) : "未知"],
          ["修改时间", o.modified && !o.modified.startsWith("0001-") ? o.modified : "未知"],
          ["扫描时间", o.scanned_at],
          [
            "可用哈希",
            Object.entries(o.hashes || {})
              .map(([k, v]) => `${k}: ${v}`)
              .join("；") || "未提供",
          ],
          [
            "哈希证据",
            o.hash_verified
              ? "本地完整计算或经验证来源"
              : "来源报告，完整性语义未验证",
          ],
        ]) {
          dl.append(node("dt", key), node("dd", String(value || "未知")));
        }
        card.append(dl);
        return card;
      }
      function filters() {
        const bar = node("div", undefined, "inventory-filters"),
          input = node("input");
        input.type = "search";
        input.placeholder = "筛选相对路径";
        input.setAttribute("aria-label", "筛选相对路径");
        input.value = query.text;
        input.onchange = () => {
          query.text = input.value;
          limit = 100;
          render();
        };
        bar.append(
          input,
          optionSelect(
            "差异分类",
            { all: "全部", ...labels },
            query.category,
            (v) => {
              query.category = v;
              limit = 100;
              render();
            },
          ),
          optionSelect(
            "复核结论",
            { all: "全部", ...decisions },
            query.review,
            (v) => {
              query.review = v;
              limit = 100;
              render();
            },
          ),
        );
        const dirs = { "": "全部目录" },
          types = { all: "全部类型", directory: "目录" };
        for (const v of result?.items || []) {
          const i = v.relative.lastIndexOf("/");
          if (i >= 0)
            dirs[v.relative.slice(0, i + 1)] = v.relative.slice(0, i + 1);
          if (!v.is_dir) {
            const ext = v.relative.match(/\.[^/.]+$/)?.[0]?.toLowerCase();
            if (ext) types[ext] = ext;
          }
        }
        bar.append(
          optionSelect("目录", dirs, query.directory, (v) => {
            query.directory = v;
            limit = 100;
            render();
          }),
          optionSelect("类型", types, query.type, (v) => {
            query.type = v;
            limit = 100;
            render();
          }),
        );
        content.append(bar);
      }
      function reviewAllowed(v) {
        return complete && !jobRunning() && !!v.detail;
      }
      function jobRunning() {
        return ["enumerating", "checking", "canceling"].includes(job?.state);
      }
      function downloadAllowed(v) {
        return (
          complete &&
          job?.state === "completed" &&
          !v.blocked &&
          decision(v) === "missing" &&
          v.detail?.source &&
          !v.detail.nas
        );
      }
      async function review(items, action, candidate = "", note = "") {
        if (busy || !items.length) return;
        if (
          items.length > 1 &&
          !window.confirm(
            `仅对明确勾选的 ${items.length} 项应用“${decisions[action] || "撤销"}”？不包含未勾选后代。`,
          )
        )
          return;
        busy = true;
        render();
        try {
          const data = await api("/admin/bitqiu/nas/scan/review", {
            scan_id: result.scan_id,
            ids: items.map((v) => v.id),
            fingerprints: Object.fromEntries(
              items.map((v) => [v.id, v.detail.fingerprint]),
            ),
            decision: action,
            candidate,
            note,
            confirmed: items.length > 1,
          });
          result = data.result;
          selected.clear();
          downloadSelected.clear();
        } finally {
          busy = false;
          render();
        }
      }
      function reviewCard(v) {
        const box = node("details", undefined, "inventory-review");
        box.append(node("summary", "并列信息、候选与复核记录"));
        box.ontoggle = () => {
          if (!box.open || box.dataset.ready) return;
          box.dataset.ready = "1";
          const d = v.detail;
          if (!d) return;
          const pair = node("div", undefined, "inventory-pair");
          pair.append(
            objectCard("Bitqiu 来源", d.source),
            objectCard("NAS 同路径", d.nas),
          );
          box.append(pair, node("p", (d.reasons || []).join("；")));
          const candidates = node("select");
          candidates.setAttribute("aria-label", "关联 NAS 候选");
          const blank = node("option", "请选择候选");
          blank.value = "";
          candidates.append(blank);
          for (const c of d.candidates || []) {
            const o = node(
              "option",
              c.object.path + " · " + c.reasons.join("；"),
            );
            o.value = c.object.path;
            candidates.append(o);
            const detail = node("details");
            detail.append(
              node("summary", c.object.path + " · " + c.reasons.join("；")),
              objectCard("NAS 候选", c.object),
            );
            box.append(detail);
          }
          box.append(candidates);
          const note = node("textarea");
          note.placeholder = "复核备注（最多 4000 字节）";
          note.maxLength = 1000;
          note.setAttribute("aria-label", "复核备注");
          box.append(note);
          const actions = node("div", undefined, "task-global");
          for (const action of [
            "missing",
            "exists",
            "ignored",
            "pending",
            "undo",
          ]) {
            const b = button(
              action === "undo" ? "撤销上一结论" : decisions[action],
              () => review([v], action, candidates.value, note.value),
            );
            b.disabled =
              busy ||
              !reviewAllowed(v) ||
              (action === "missing" && (v.blocked || !d.source || !!d.nas));
            actions.append(b);
          }
          box.append(actions);
          for (const e of [...(d.reviews || [])].reverse()) {
            box.append(
              node(
                "p",
                `${e.at} · ${e.reviewer_name} · ${decisions[e.decision] || e.decision}${e.candidate ? " → " + e.candidate : ""} · ${e.note || "无备注"}`,
                "hint",
              ),
            );
          }
          if (d.stale)
            box.append(
              node("p", "证据已变化，旧结论需重新确认。", "task-error"),
            );
        };
        return box;
      }
      function rows(items, mode) {
        const visible = items.slice(0, limit);
        content.append(
          node(
            "p",
            `共 ${items.length} 项，显示 ${visible.length} 项；疑似项默认不选。`,
            "hint",
          ),
        );
        for (const v of visible) {
          const row = node("article", undefined, "inventory-row");
          let selectSet = mode === "download" ? downloadSelected : selected;
          if (mode === "review" || mode === "download") {
            const disabled =
              mode === "download" ? !downloadAllowed(v) : !reviewAllowed(v);
            const check = checkbox(v.relative, selectSet.has(v.id), (on) => {
              if (on) selectSet.add(v.id);
              else selectSet.delete(v.id);
              render();
            });
            check.querySelector("input").disabled = disabled;
            row.append(check);
          } else row.append(node("strong", v.relative));
          row.append(
            node(
              "p",
              `${labels[v.category] || v.category} · ${v.is_dir ? "目录" : v.size >= 0 ? bytes(v.size) : "大小未知"} · ${decisions[decision(v)]}${v.detail?.stale ? " · 需重新确认" : ""}`,
            ),
          );
          if (v.detail) row.append(reviewCard(v));
          content.append(row);
        }
        if (items.length > limit)
          content.append(
            button("显示更多", () => {
              limit += 100;
              render();
            }),
          );
      }
      function summary() {
        const source = (result?.inventory || []).filter(
            (v) => v.side === "bitqiu",
          ),
          nas = (result?.inventory || []).filter((v) => v.side === "nas");
        const cards = node("div", undefined, "inventory-pair");
        for (const [title, objects] of [
          ["Bitqiu", source],
          ["NAS", nas],
        ]) {
          const card = node("section", undefined, "inventory-card"),
            files = objects.filter((v) => !v.is_dir),
            known = files.filter((v) => v.size_known);
          card.append(
            node("h3", title),
            node(
              "p",
              `${objects.length - files.length} 个目录 · ${files.length} 个文件 · 已知容量 ${bytes(known.reduce((n, v) => n + v.size, 0))} · 未知大小 ${files.length - known.length}`,
            ),
          );
          const types = {};
          for (const f of files) {
            const ext =
              f.path.match(/\.[^/.]+$/)?.[0]?.toLowerCase() || "无扩展名";
            types[ext] = (types[ext] || 0) + 1;
          }
          card.append(
            node(
              "p",
              Object.entries(types)
                .map(([t, n]) => `${t}: ${n}`)
                .join(" · "),
            ),
          );
          cards.append(card);
        }
        content.append(cards);
        const groups = new Map();
        for (const o of nas) {
          const h = o.hashes?.sha256;
          if (h) {
            if (!groups.has(h)) groups.set(h, []);
            groups.get(h).push(o.path);
          }
        }
        const duplicates = [...groups.values()].filter((v) => v.length > 1);
        content.append(
          node("h3", `重复候选汇总：${duplicates.length} 组`),
          node(
            "p",
            "仅汇总完整 SHA-256 相同的 NAS 候选，不自动移动、重命名或删除。",
            "hint",
          ),
        );
        for (const paths of duplicates)
          content.append(node("p", paths.join("；")));
      }
      function issues() {
        const all = [
          ...(result?.issues || []),
          ...(attempt?.scan_id !== result?.scan_id
            ? attempt?.issues || []
            : []),
        ];
        content.append(node("h3", `扫描问题 ${all.length}`));
        for (const p of all)
          content.append(
            node("p", `${p.relative || "根目录"}：${p.message}`, "task-error"),
          );
      }
      function render() {
        for (const b of tabs.children)
          b.setAttribute("aria-selected", String(b.dataset.tab === tab));
        content.replaceChildren();
        const status = job
          ? `任务：${job.state} · 范围：${job.source} → ${job.destination} · ${(job.names || []).join("、")}`
          : "尚未开始盘点";
        content.append(node("p", status, "hint"));
        if (tab === "scope") {
          content.append(
            node("h3", "来源与 NAS 按相对路径映射"),
            node(
              "p",
              `当前选择：${state.source || "请选择来源目录"} → ${state.target || "请选择 NAS 目录"}；${[...state.selected].join("、") || "请在左侧勾选文件或文件夹"}`,
            ),
            form,
            node(
              "p",
              "目录中的后代保留相对路径。跨目录候选仅来自本次选定范围。NAS 通过容器挂载的本地存储访问。",
              "hint",
            ),
          );
          return;
        }
        if (!result) {
          content.append(node("p", "请先完成一次所选范围的双端盘点。"));
          issues();
          return;
        }
        if (!complete)
          content.append(
            node(
              "p",
              "本次为不完整盘点，仅供查看和导出；复核及补缺不可用。",
              "task-error",
            ),
          );
        content.append(
          node(
            "p",
            `结果：${result.scan_id} · 完成时间 ${result.completed_at}${job?.state !== "completed" ? " · 保留的最近完整缓存，下载暂不可用" : ""}`,
            "hint",
          ),
        );
        content.append(
          node(
            "p",
            `本结果选项：NAS 独有 ${result.options?.nas_only ? "开启" : "关闭"} · 跨目录 ${result.options?.cross_directory ? "开启" : "关闭"} · NAS 完整哈希 ${result.options?.full_hash ? "开启" : "关闭"}`,
            "hint",
          ),
        );
        if (tab === "inventory") {
          summary();
          const d = node("details");
          d.append(
            node(
              "summary",
              `全部盘点对象 ${(result.inventory || []).length} 项`,
            ),
          );
          d.ontoggle = () => {
            if (!d.open || d.dataset.ready) return;
            d.dataset.ready = "1";
            for (const o of (result.inventory || []).slice(0, 200))
              d.append(objectCard(o.side === "nas" ? "NAS" : "Bitqiu", o));
            if ((result.inventory || []).length > 200)
              d.append(
                node("p", "此处显示前 200 项；完整对象列表可导出 JSON。"),
              );
          };
          content.append(d);
          issues();
          return;
        }
        if (tab === "export") {
          content.append(node("h3", "导出报告"));
          const groups = new Set([
            "suspected_missing",
            "confirmed_missing",
            "pending",
            "other",
            "issues",
          ]);
          for (const [key, title] of [
            ["suspected_missing", "疑似缺失"],
            ["confirmed_missing", "已确认缺失"],
            ["pending", "待复核"],
            ["other", "其他差异"],
            ["issues", "扫描问题"],
          ])
            content.append(
              checkbox(title, true, (on) => {
                if (on) groups.add(key);
                else groups.delete(key);
              }),
            );
          for (const format of ["csv", "json"])
            content.append(
              button("导出 " + format.toUpperCase(), async () => {
                const response = await fetch(
                  base + "/api/admin/bitqiu/nas/scan/export",
                  {
                    method: "POST",
                    headers: headers(),
                    body: JSON.stringify({
                      scan_id: result.scan_id,
                      format,
                      groups: [...groups],
                    }),
                  },
                );
                if (
                  !response.ok ||
                  (response.headers
                    .get("Content-Type")
                    ?.includes("application/json") &&
                    format === "csv")
                ) {
                  const err = await response.json();
                  throw Error(err.message || "导出失败");
                }
                const blob = await response.blob();
                if (format === "json") {
                  const check = JSON.parse(await blob.text());
                  if (check.code && check.code !== 200)
                    throw Error(check.message);
                }
                const url = URL.createObjectURL(blob),
                  a = node("a");
                a.href = url;
                a.download = "nas-inventory." + format;
                a.click();
                setTimeout(() => URL.revokeObjectURL(url), 1000);
              }),
            );
          return;
        }
        filters();
        let items = filter(result.items || []);
        if (tab === "review") {
          content.append(
            node(
              "p",
              `当前明确勾选 ${selected.size} 项；批量操作仅作用于这些项目，不包含后代。`,
            ),
          );
          const note = node("textarea");
          note.placeholder = "批量复核备注";
          note.maxLength = 1000;
          note.setAttribute("aria-label", "批量复核备注");
          content.append(note);
          const actions = node("div", undefined, "task-global");
          for (const action of ["missing", "ignored", "pending", "undo"])
            actions.append(
              button(
                action === "undo" ? "批量撤销" : "批量" + decisions[action],
                () =>
                  review(
                    (result.items || []).filter((v) => selected.has(v.id)),
                    action,
                    "",
                    note.value,
                  ),
              ),
            );
          actions.append(
            button("取消勾选", () => {
              selected.clear();
              render();
            }),
          );
          content.append(actions);
          rows(items, "review");
          return;
        }
        if (tab === "download") {
          items = items.filter(downloadAllowed);
          content.append(
            node(
              "p",
              `仅展示当前证据下人工确认缺失的项目。已明确选择 ${downloadSelected.size} 项；不会因选择目录而隐式包含后代。`,
            ),
          );
          content.append(
            button("取消补缺选择", () => {
              downloadSelected.clear();
              render();
            }),
          );
          const b = button("生成最终去重清单", () =>
            prepare(result.scan_id, [...downloadSelected]),
          );
          b.disabled =
            busy || !downloadSelected.size || job?.state !== "completed";
          content.append(b);
          rows(items, "download");
          return;
        }
        rows(items, "differences");
        issues();
      }
      render();
      return {
        options: () => ({ ...options }),
        update(data) {
          job = data?.job || job;
          attempt = data?.attempt || null;
          if (data?.result) {
            result = data.result;
            selected.clear();
            downloadSelected.clear();
            complete = true;
          } else if (!result && attempt) {
            result = attempt;
            complete = false;
          }
          if (data?.job === null) {
            job = null;
            result = null;
            attempt = null;
            selected.clear();
            downloadSelected.clear();
          }
          render();
        },
        scope() {
          if (tab === "scope") render();
        },
      };
    },
  };
})();
