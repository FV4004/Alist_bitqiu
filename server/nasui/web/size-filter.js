/* Size filtering is local to loaded picker entries; it never changes selection. */
(() => {
  "use strict";
  // Match the existing NAS picker and download size display (1024-based MB).
  const MB = 1024 * 1024;
  const MAX_MB = Math.floor(Number.MAX_SAFE_INTEGER / MB);
  const positions = [0, 150, 350, 550, 750, 950];
  function parseBound(raw) {
    const text = String(raw ?? "").trim();
    if (!text) return null;
    if (!/^(?:\d+(?:\.\d{0,6})?|\.\d{1,6})$/.test(text)) {
      throw Error("请输入非负 MB 数值，最多支持 6 位小数；留空表示不限。");
    }
    const value = Number(text);
    if (!Number.isFinite(value) || value > MAX_MB)
      throw Error("大小超出支持范围，请填写更小的数值。");
    return value;
  }
  const knownSize = (file) =>
    file.size_known !== false &&
    typeof file.size === "number" &&
    Number.isSafeInteger(file.size) &&
    file.size >= 0;
  class SizeFilter {
    constructor() {
      this.min = 80;
      this.max = null;
      this.scopes = new Set(["source", "target"]);
      this.domain = 10000;
    }
    active(side) {
      return this.scopes.has(side) && (this.min !== null || this.max !== null);
    }
    key(side) {
      return this.scopes.has(side) ? [this.min, this.max] : null;
    }
    matches(file, side) {
      if (file.is_dir || !this.active(side)) return true;
      if (!knownSize(file)) return false;
      return (
        (this.min === null || file.size >= this.min * MB) &&
        (this.max === null || file.size <= this.max * MB)
      );
    }
    setBounds(min, max) {
      const a = parseBound(min),
        b = parseBound(max);
      if (a !== null && b !== null && a > b)
        throw Error("最小值不能大于最大值；修正前继续应用上一有效范围。");
      this.min = a;
      this.max = b;
    }
    updateDomain(files) {
      let largest = Math.max(10000, this.min || 0, this.max || 0);
      for (const file of files)
        if (!file.is_dir && knownSize(file))
          largest = Math.max(largest, file.size / MB);
      this.domain = Math.min(MAX_MB, Math.ceil(largest / 1000) * 1000);
    }
    stops() {
      return [0, 80, 500, 1000, 5000, this.domain];
    }
    toPosition(value, edge) {
      if (value === null) return edge === "max" ? 1000 : 0;
      const stops = this.stops();
      for (let i = 1; i < stops.length; i++)
        if (value <= stops[i]) {
          return (
            positions[i - 1] +
            ((value - stops[i - 1]) / (stops[i] - stops[i - 1])) *
              (positions[i] - positions[i - 1])
          );
        }
      return 950;
    }
    fromPosition(position, edge) {
      if (edge === "max" && position > 950) return null;
      const p = Math.max(0, Math.min(950, position)),
        stops = this.stops();
      for (let i = 1; i < positions.length; i++)
        if (p <= positions[i]) {
          return (
            Math.round(
              (stops[i - 1] +
                ((p - positions[i - 1]) / (positions[i] - positions[i - 1])) *
                  (stops[i] - stops[i - 1])) *
                100,
            ) / 100
          );
        }
      return this.domain;
    }
    move(edge, position) {
      let value = this.fromPosition(position, edge);
      if (edge === "min") {
        if (this.max !== null) value = Math.min(value, this.max);
        this.min = value;
      } else {
        if (value !== null && this.min !== null)
          value = Math.max(value, this.min);
        this.max = value;
      }
    }
  }
  function mount(model, { document, onChange }) {
    const $ = (id) => document.getElementById(id),
      root = $("size-filter");
    const min = $("size-min"),
      max = $("size-max"),
      error = $("size-error");
    const display = (value) =>
      value === null
        ? "不限"
        : value.toLocaleString("zh-CN", { maximumFractionDigits: 6 }) + " MB";
    function render(writeInputs = false) {
      if (writeInputs) {
        min.value = model.min === null ? "" : String(model.min);
        max.value = model.max === null ? "" : String(model.max);
      }
      const a = model.toPosition(model.min, "min"),
        b = model.toPosition(model.max, "max");
      $("size-min-slider").value = String(a);
      $("size-max-slider").value = String(b);
      $("size-min-slider").setAttribute("aria-valuetext", display(model.min));
      $("size-max-slider").setAttribute("aria-valuetext", display(model.max));
      $("size-min-label").textContent = display(model.min);
      $("size-max-label").textContent = display(model.max);
      root.style.setProperty("--size-start", a / 10 + "%");
      root.style.setProperty("--size-end", b / 10 + "%");
      const range =
        model.min === null && model.max === null
          ? "不限大小"
          : model.min === null
            ? `≤ ${display(model.max)} · 最小值不限`
            : model.max === null
              ? `≥ ${display(model.min)} · 最大值不限`
              : `${display(model.min)} – ${display(model.max)}`;
      $("size-summary").textContent = "当前范围：" + range;
      $("size-scope-status").textContent =
        model.scopes.size === 0
          ? "未应用"
          : model.scopes.size === 2
            ? "已应用于两侧"
            : "仅应用于" + (model.scopes.has("source") ? "网盘" : "NAS");
    }
    function clearError() {
      error.hidden = true;
      error.textContent = "";
      min.removeAttribute("aria-invalid");
      max.removeAttribute("aria-invalid");
    }
    function input() {
      try {
        model.setBounds(min.value, max.value);
        clearError();
        model.updateDomain(files);
        render();
        onChange();
      } catch (e) {
        error.textContent = e.message;
        error.hidden = false;
        min.setAttribute("aria-invalid", "true");
        max.setAttribute("aria-invalid", "true");
      }
    }
    let files = [];
    min.addEventListener("input", input);
    max.addEventListener("input", input);
    for (const edge of ["min", "max"])
      $("size-" + edge + "-slider").addEventListener("input", (event) => {
        model.move(edge, Number(event.target.value));
        clearError();
        render(true);
        onChange();
      });
    // One keyboard step leaves the infinity endpoint for the largest finite
    // value; snapping the infinity zone must not trap ArrowLeft at infinity.
    $("size-max-slider").addEventListener("keydown", (event) => {
      if (
        model.max === null &&
        ["ArrowLeft", "ArrowDown"].includes(event.key)
      ) {
        event.preventDefault();
        model.move("max", 950);
        clearError();
        render(true);
        onChange();
      }
    });
    $("size-clear").onclick = () => {
      model.setBounds("", "");
      clearError();
      render(true);
      onChange();
    };
    for (const side of ["source", "target"])
      $("size-" + side).onchange = (event) => {
        if (event.target.checked) model.scopes.add(side);
        else model.scopes.delete(side);
        render();
        onChange();
      };
    render(true);
    return {
      refresh(source, target) {
        files = [...source, ...target];
        model.updateDomain(files);
        render();
        for (const [side, entries] of [
          ["source", source],
          ["target", target],
        ]) {
          const count = entries.filter(
              (file) => !file.is_dir && !knownSize(file),
            ).length,
            status = $(side + "-size-status");
          status.hidden = !count || !model.active(side);
          status.textContent = count
            ? `大小未知的文件：${count} 项，当前未显示`
            : "";
        }
      },
    };
  }
  const api = { MB, SizeFilter, parseBound, knownSize, mount };
  if (typeof module !== "undefined" && module.exports) module.exports = api;
  else window.NASSizeFilter = api;
})();
