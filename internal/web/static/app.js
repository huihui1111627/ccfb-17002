let state = { report: null, acceptance: null, scrubT: 0 };

const $ = (id) => document.getElementById(id);
const esc = (s) => String(s ?? "").replace(/[&<>"']/g, (c) => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" }[c]));

function fmtClock(t) {
  if (t === null || t === undefined) return "未完成";
  const m = Math.floor(t / 60);
  const s = Math.floor(t % 60);
  return `T+${String(m).padStart(2, "0")}:${String(s).padStart(2, "0")}`;
}
function fmtDuration(sec) {
  if (!sec && sec !== 0) return "—";
  const m = Math.floor(sec / 60);
  const s = sec % 60;
  return m ? `${m}分${String(s).padStart(2, "0")}秒` : `${s}秒`;
}
function pct(t, maxT) {
  return Math.max(0, Math.min(100, (t / maxT) * 100));
}
function rangeStyle(a, b, maxT) {
  const left = pct(a, maxT);
  const width = Math.max(0.35, pct(b, maxT) - left);
  return `left:${left}%;width:${width}%`;
}

async function loadSim() {
  const variant = parseInt($("variant").value || "0", 10);
  const seed = parseInt($("seed").value || "20261127", 10);
  const res = await fetch(`/api/sim?variant=${encodeURIComponent(variant)}&seed=${encodeURIComponent(seed)}`);
  state.report = await res.json();
  const sc = state.report.scenario;
  state.scrubT = Math.min(state.report.endAt || 3600, sc.mainsAt + 480);
  $("scrub").max = state.report.endAt || 3600;
  $("scrub").value = state.scrubT;
  renderAll();
}

function renderAll() {
  renderKpis();
  renderChart();
  renderTimeline();
  renderWaits();
  renderOrders();
  renderEvents();
  updateMoment();
}

function renderKpis() {
  const r = state.report;
  const refOk = r.referenceMatch;
  const cards = [
    { name: "来电后总恢复时间", value: fmtDuration(r.totalRecoverySeconds), hint: `完成于 ${fmtClock(r.recoveryCompleteAt)}（模拟绝对时间）`, cls: r.success ? "good" : "bad" },
    { name: "数据库压力峰值", value: `${r.maxDbPressure.toFixed(1)}%`, hint: `硬上限 90%；违规 ${r.pressureViolations.length}`, cls: r.maxDbPressure <= 90 ? "good" : "bad" },
    { name: "缓存压力峰值", value: `${r.maxCachePressure.toFixed(1)}%`, hint: "含预热与终端缓存访问", cls: r.maxCachePressure <= 90 ? "good" : "bad" },
    { name: "依赖顺序", value: r.dependencyViolations.length ? `${r.dependencyViolations.length} 违规` : "0 违规", hint: "启动与放流均等待真实就绪", cls: r.dependencyViolations.length ? "bad" : "good" },
    { name: "参照实现时间线", value: refOk ? "逐项一致" : "不一致", hint: refOk ? "64 组验收也可复跑" : esc(r.referenceDiff), cls: refOk ? "good" : "bad" },
    { name: "终端 / 异常单", value: `${r.scenario.terminalTotal} / ${r.reconciliation.mismatchOrders}`, hint: `机器 ${r.scenario.machines.length} · 服务 ${r.scenario.services.length}`, cls: r.reconciliation.mismatchOrders ? "warn" : "good" },
  ];
  $("kpis").innerHTML = cards.map(c => `<div class="kpi ${c.cls}"><div class="name">${c.name}</div><div class="value">${c.value}</div><div class="hint">${c.hint}</div></div>`).join("");
}

function renderChart() {
  const r = state.report;
  const maxT = r.endAt || 3600;
  const W = 1000, H = 280, L = 46, R = 18, T = 18, B = 30;
  const x = t => L + pct(t, maxT) / 100 * (W - L - R);
  const y = v => H - B - Math.min(105, v) / 105 * (H - T - B);
  const samples = r.samples || [];
  const line = key => samples.map((s, i) => `${i ? "L" : "M"}${x(s.t).toFixed(1)},${y(s[key]).toFixed(1)}`).join(" ");
  const area = key => `${line(key)} L${x(samples.at(-1)?.t || maxT)},${y(0)} L${x(samples[0]?.t || 0)},${y(0)} Z`;
  const grid = [0, 25, 50, 75, 90, 100].map(v => `
    <line x1="${L}" x2="${W-R}" y1="${y(v)}" y2="${y(v)}" stroke="${v === 90 ? "#d92d20" : "#e4eaf3"}" stroke-dasharray="${v === 90 ? "6 5" : "0"}"/>
    <text x="${L-8}" y="${y(v)+4}" text-anchor="end" font-size="11" fill="#667085">${v}</text>`).join("");
  const ticks = Array.from({ length: Math.floor(maxT / 300) + 1 }, (_, i) => i * 300).map(t => `
    <line x1="${x(t)}" x2="${x(t)}" y1="${T}" y2="${H-B}" stroke="#eef2f7"/>
    <text x="${x(t)+3}" y="${H-10}" font-size="10" fill="#667085">${fmtClock(t).replace("T+", "")}</text>`).join("");
  const sc = r.scenario;
  const stable = r.items.find(i => i.kind === "voltage_stable");
  $("pressureChart").innerHTML = `
  <svg id="chartSvg" viewBox="0 0 ${W} ${H}" role="img" aria-label="压力曲线">
    <rect x="${L}" y="${T}" width="${x(sc.mainsAt)-L}" height="${H-T-B}" fill="#d9820b" opacity=".08"/>
    <rect x="${x(sc.outageStart)}" y="${T}" width="${x(sc.outageEnd)-x(sc.outageStart)}" height="${H-T-B}" fill="#d92d20" opacity=".10"/>
    ${grid}${ticks}
    <path d="${area("dbPressure")}" fill="#d92d20" opacity=".10"/>
    <path d="${line("dbPressure")}" fill="none" stroke="#d92d20" stroke-width="2.4"/>
    <path d="${line("cachePressure")}" fill="none" stroke="#7a5af8" stroke-width="2.2"/>
    <path d="${line("upsChargePct")}" fill="none" stroke="#12a150" stroke-width="2" stroke-dasharray="5 4"/>
    ${stable ? `<line x1="${x(stable.t)}" x2="${x(stable.t)}" y1="${T}" y2="${H-B}" stroke="#12a150" stroke-dasharray="4 4"/><text x="${x(stable.t)+4}" y="${T+10}" font-size="10" fill="#12a150">电压稳</text>` : ""}
    <rect id="chartHover" x="${L}" y="${T}" width="${W-L-R}" height="${H-T-B}" fill="transparent"/>
    <line id="chartScrub" x1="${x(state.scrubT)}" x2="${x(state.scrubT)}" y1="${T}" y2="${H-B}" stroke="#172033" stroke-width="1.4" opacity=".45"/>
  </svg>`;
  $("chartSvg").addEventListener("mousemove", (e) => {
    const svg = $("chartSvg");
    const rect = svg.getBoundingClientRect();
    const vx = (e.clientX - rect.left) / rect.width * W;
    let t = Math.round(((vx - L) / (W - L - R)) * maxT / 5) * 5;
    t = Math.max(0, Math.min(maxT, t));
    state.scrubT = t;
    $("scrub").value = t;
    updateMoment();
    document.querySelectorAll(".scrub-line").forEach(el => el.style.left = `${pct(t, maxT)}%`);
    const cs = $("chartScrub");
    if (cs) cs.setAttribute("x1", x(t)), cs.setAttribute("x2", x(t));
  });
}

function markersFor(subject, maxT) {
  const r = state.report;
  const spec = {
    machine_disk_failed: ["fail", "◆"],
    machine_restart: ["fail", "↻"],
    machine_quarantine: ["fail", "⛔"],
    machine_graceful_stop: ["migrate", "■"],
    machine_power_off: ["fail", "■"],
    standby_verified: ["migrate", "✓"],
    standby_verify_failed: ["fail", "◆"],
    service_start: ["start", "▸"],
    service_start_failed: ["fail", "◆"],
    service_graceful_stop: ["migrate", "■"],
    service_relocated: ["migrate", "●"],
    readiness_passed: ["ready", "✓"],
    traffic_open: ["traffic", "▶"],
    reconcile_mismatch: ["fail", "!"],
  };
  return r.items.filter(i => i.subject === subject && spec[i.kind]).map(i => {
    const [cls, icon] = spec[i.kind];
    return `<span class="marker ${cls}" style="left:${pct(i.t, maxT)}%" title="${esc(fmtClock(i.t) + " " + i.label + "：" + i.detail)}">${icon}</span>`;
  }).join("");
}

function scrubLine(maxT) {
  return `<span class="scrub-line" style="left:${pct(state.scrubT, maxT)}%"></span>`;
}

function renderTimeline() {
  const r = state.report;
  const maxT = r.endAt || 3600;
  const q = $("filterText").value.trim().toLowerCase();
  const onlyProblems = $("onlyProblems").checked;
  const onlyCritical = $("onlyCritical").checked;
  const machineMap = Object.fromEntries(r.machineTimings.map(m => [m.id, m]));
  const svcMap = Object.fromEntries(r.serviceTimings.map(s => [s.id, s]));

  const matchText = (...xs) => !q || xs.join(" ").toLowerCase().includes(q);
  const machineProblem = m => m.diskFailedAt != null || m.quarantinedAt != null || m.specStatus === "disk_failed" || m.specStatus === "quarantined";
  const serviceProblem = s => s.attempts > 1 || s.originalMachineId !== s.machineId;

  const ticks = Array.from({ length: Math.floor(maxT / 300) + 1 }, (_, i) => i * 300)
    .map(t => `<span class="tick" style="left:${pct(t, maxT)}%">${fmtClock(t)}</span>`).join("");

  let html = `<div class="tl-axis"><div class="tl-label">对象 / 阶段</div><div class="axis-track">${ticks}</div></div>`;

  // 供电上下文行。
  html += `<div class="tl-row power-row">
    <div class="tl-label">供电与 UPS<small>发电机 / 市电 / 观察窗</small></div>
    <div class="tl-track">
      <span class="power-interval gen" style="${rangeStyle(0, r.scenario.mainsAt, maxT)}" title="发电机供电"></span>
      <span class="power-interval outage" style="${rangeStyle(r.scenario.outageStart, r.scenario.outageEnd, maxT)}" title="再次停电"></span>
      ${r.items.filter(i => ["mains_restored", "mains_lost", "ups_ready", "voltage_stable"].includes(i.kind)).map(i =>
        `<span class="marker power" style="left:${pct(i.t, maxT)}%" title="${esc(fmtClock(i.t) + " " + i.label + "：" + i.detail)}">▣</span>`).join("")}
      ${scrubLine(maxT)}
    </div></div>`;

  const machines = r.machineTimings.filter(m => {
    if (!matchText(m.id, m.name, m.role)) return false;
    if (onlyProblems && !machineProblem(m)) return false;
    if (onlyCritical && !(m.role === "database" || m.role === "settlement")) return false;
    return true;
  });
  html += `<div class="tl-group"><div class="group-title">机器开机时间轴（${machines.length}）</div></div>`;
  html += machines.map(m => {
    let bar = "";
    if (m.bootStartAt != null) {
      const end = m.bootedAt ?? m.diskFailedAt ?? m.quarantinedAt ?? maxT;
      const cls = (m.diskFailedAt != null) ? "fail" : "boot";
      bar = `<span class="bar ${cls}" style="${rangeStyle(m.bootStartAt, end, maxT)}" title="${esc(m.name + " 引导：" + fmtClock(m.bootStartAt) + " → " + fmtClock(end))}"></span>`;
    }
    return `<div class="tl-row">
      <div class="tl-label" title="${esc(m.id)}">${esc(m.name)}<small>${esc(m.id)} · ${esc(m.specStatus)}${m.generatorBacked ? " · 发电保障" : ""}</small></div>
      <div class="tl-track">${bar}${markersFor(m.id, maxT)}${scrubLine(maxT)}</div>
    </div>`;
  }).join("");

  const services = r.serviceTimings.filter(s => {
    if (!matchText(s.id, s.name, s.kind)) return false;
    if (onlyProblems && !serviceProblem(s)) return false;
    if (onlyCritical && !(s.critical || ["database", "cache", "settlement", "gateway"].includes(s.kind))) return false;
    return true;
  });
  html += `<div class="tl-group"><div class="group-title">服务启动 / 真就绪 / 放流时间轴（${services.length}）</div></div>`;
  html += services.map(s => {
    let bars = "";
    if (s.firstStartAt != null) {
      // 条显示最后一次（通常也是成功那次）启动到最终就绪；所有尝试由 ▸/◆ 标记补全。
      const barStart = s.lastStartAt ?? s.firstStartAt;
      const readyEnd = s.readyAt ?? r.recoveryCompleteAt ?? maxT;
      bars += `<span class="bar starting" style="${rangeStyle(barStart, readyEnd, maxT)}" title="${esc(s.name + " 最近启动/探测：" + fmtClock(barStart) + " → " + fmtClock(s.readyAt) + "；首次启动 " + fmtClock(s.firstStartAt))}"></span>`;
    }
    if (s.readyAt != null && s.trafficAt != null) {
      bars += `<span class="bar ready" style="${rangeStyle(s.readyAt, s.trafficAt, maxT)}" title="已真就绪、预热或核对：${fmtClock(s.readyAt)} → ${fmtClock(s.trafficAt)}"></span>`;
    } else if (s.readyAt != null) {
      bars += `<span class="bar ready" style="${rangeStyle(s.readyAt, r.recoveryCompleteAt || maxT, maxT)}"></span>`;
    }
    if (s.trafficAt != null) {
      bars += `<span class="bar traffic" style="${rangeStyle(s.trafficAt, r.recoveryCompleteAt || maxT, maxT)}" title="开始接流量：${fmtClock(s.trafficAt)}"></span>`;
    }
    const dot = s.critical ? "🔴" : (!s.abruptSafe ? "🟠" : "");
    return `<div class="tl-row">
      <div class="tl-label" title="${esc(s.id)}">${dot} ${esc(s.name)}<small>${esc(s.id)} · L${s.layer} · ${esc(s.status)}${s.originalMachineId !== s.machineId ? ` · ${esc(s.originalMachineId)}→${esc(s.machineId)}` : ` · ${esc(s.machineId)}`}</small></div>
      <div class="tl-track">${bars}${markersFor(s.id, maxT)}${scrubLine(maxT)}</div>
    </div>`;
  }).join("");

  $("timeline").innerHTML = html;
}

function sampleAt(t) {
  const samples = state.report.samples || [];
  let cur = samples[0];
  for (const s of samples) if (s.t <= t) cur = s;
  return cur || {};
}

function updateMoment() {
  const t = parseInt($("scrub").value || "0", 10);
  state.scrubT = t;
  $("clock").textContent = fmtClock(t);
  const s = sampleAt(t);
  $("scrubHint").textContent = s.mains ? (s.voltageStable ? "市电 · 电压稳定" : "市电 · 十分钟电压观察窗内") : "发电机 / 停电中";
  const stats = [
    ["DB 压力", `${(s.dbPressure ?? 0).toFixed?.(1) ?? 0}%`],
    ["缓存压力", `${(s.cachePressure ?? 0).toFixed?.(1) ?? 0}%`],
    ["UPS", `${(s.upsChargePct ?? 0).toFixed?.(0) ?? 0}%${s.upsReady ? " ✓" : ""}`],
    ["终端在线", `${s.connectedTerminals ?? 0}`],
    ["启动/探测", `${s.startingServices ?? 0}`],
    ["已放流", `${s.openServices ?? 0}`],
  ];
  $("momentStats").innerHTML = stats.map(([n, v]) => `<div class="moment-stat"><b>${v}</b><span>${n}</span></div>`).join("");
  renderWaits();
  document.querySelectorAll(".scrub-line").forEach(el => el.style.left = `${pct(t, state.report.endAt || 3600)}%`);
  const cs = $("chartScrub");
  if (cs && state.report) {
    const maxT = state.report.endAt || 3600, W = 1000, L = 46, R = 18;
    const x = tt => L + pct(tt, maxT) / 100 * (W - L - R);
    cs.setAttribute("x1", x(t)); cs.setAttribute("x2", x(t));
  }
}

function renderWaits() {
  if (!state.report) return;
  const t = state.scrubT;
  const critical = Object.fromEntries(state.report.serviceTimings.map(s => [s.id, s.critical]));
  const active = state.report.waits.filter(w => w.start <= t && (!w.end || w.end >= t)).sort((a, b) => a.start - b.start);
  if (!active.length) {
    $("waitList").innerHTML = `<div class="empty">${fmtClock(t)} 没有服务被规则卡住；可能正在执行启动、真实就绪探测或已接流量。</div>`;
    return;
  }
  $("waitList").innerHTML = active.map(w => `
    <div class="wait-item ${critical[w.serviceId] ? "critical" : ""}">
      <div class="title"><span>${esc(w.name)} <small class="muted">${esc(w.serviceId)}</small></span><time>${fmtClock(w.start)} → ${fmtClock(w.end)}</time></div>
      <div class="reason">${esc(w.reason)}</div>
    </div>`).join("");
}

function renderOrders() {
  const r = state.report;
  const rec = r.reconciliation;
  $("reconcileSummary").innerHTML = `
    <span class="pill">未结收购单 ${rec.totalOrders}</span>
    <span class="pill green">核对一致 ${rec.matchedOrders}</span>
    <span class="pill red">异常交人工 ${rec.mismatchOrders}</span>
    <span class="pill ${rec.handedToManual ? "amber" : "green"}">${rec.handedToManual ? "人工队列已生成" : "无需人工"}</span>`;
  $("orderRows").innerHTML = r.scenario.orders.map(o => `
    <tr>
      <td><b>${esc(o.id)}</b></td><td>${esc(o.siteName)}</td><td>${esc(o.item)}</td>
      <td>${o.mismatch ? esc(o.reason) : `上报/入库均为 ${o.expectedKg}kg`}</td>
      <td>${o.mismatch ? '<span class="badge red">待人工</span>' : '<span class="badge green">一致</span>'}</td>
    </tr>`).join("");
}

function renderEvents() {
  const q = $("filterText").value.trim().toLowerCase();
  const items = state.report.items.filter(i => !q || `${i.t} ${i.kind} ${i.subject} ${i.label} ${i.detail}`.toLowerCase().includes(q));
  $("eventLog").innerHTML = items.map(i => {
    const bad = /fail|lost|disk|quarantine|mismatch/i.test(i.kind);
    const good = /ready|passed|complete|verified|done|restored/i.test(i.kind);
    return `<div class="event ${bad ? "bad" : good ? "good" : ""}">
      <time>${fmtClock(i.t)}</time><span class="kind" title="${esc(i.kind)}">${esc(i.kind)}</span>
      <span><b>${esc(i.label)}</b> <span class="muted">${esc(i.detail)}</span></span>
    </div>`;
  }).join("");
}

async function loadAcceptance() {
  const btn = $("acceptBtn");
  btn.disabled = true; btn.textContent = "推演中…";
  try {
    const res = await fetch("/api/acceptance?runs=64");
    state.acceptance = await res.json();
    renderAcceptance();
  } finally {
    btn.disabled = false; btn.textContent = "重新运行 64 组验收";
  }
}

function renderAcceptance() {
  const a = state.acceptance;
  $("acceptancePanel").hidden = false;
  $("acceptSummary").innerHTML = `
    <span class="${a.allPassed ? "pass" : "fail-text"}">${a.passed}/${a.count} 通过</span>
    · 参照一致 ${a.referenceMatches}/${a.count}
    · 恢复 min/median/p95/max = ${a.minRecoverySeconds}/${a.p50RecoverySeconds}/${a.p95RecoverySeconds}/${a.maxRecoverySeconds}s
    · DB 峰值 ${a.maxDbPressure.toFixed(1)}% / Cache ${a.maxCachePressure.toFixed(1)}%`;
  $("acceptRows").innerHTML = a.results.map(r => `
    <tr>
      <td>${r.variant}<br><small class="muted">${r.seed}</small></td>
      <td>${r.machines}台/${r.services}服务</td>
      <td>${r.terminals}</td>
      <td>${r.totalRecoverySeconds}</td>
      <td>${r.items}</td>
      <td>${r.maxDbPressure.toFixed(1)}%</td>
      <td>${r.maxCachePressure.toFixed(1)}%</td>
      <td class="${r.referenceMatch ? "pass" : "fail-text"}">${r.referenceMatch ? "一致" : "不一致"}</td>
      <td class="${r.dependencyViolations.length ? "fail-text" : "pass"}">${r.dependencyViolations.length}</td>
      <td class="${r.pressureViolations.length ? "fail-text" : "pass"}">${r.pressureViolations.length}</td>
      <td class="${r.budgetMet ? "pass" : "fail-text"}">${r.budgetMet ? "≤3600s" : "超预算"}</td>
      <td title="${esc(r.referenceDiff)}">${r.referenceDiff ? "查看" : "—"}</td>
    </tr>`).join("");
  $("acceptancePanel").scrollIntoView({ behavior: "smooth" });
}

$("loadBtn").addEventListener("click", loadSim);
$("acceptBtn").addEventListener("click", loadAcceptance);
$("scrub").addEventListener("input", updateMoment);
$("filterText").addEventListener("input", () => { renderTimeline(); renderEvents(); });
$("onlyProblems").addEventListener("change", renderTimeline);
$("onlyCritical").addEventListener("change", renderTimeline);

(function initControlsFromQuery() {
  const q = new URLSearchParams(window.location.search);
  if (q.get("variant")) $("variant").value = q.get("variant");
  if (q.get("seed")) $("seed").value = q.get("seed");
  if (q.get("problems") === "1") $("onlyProblems").checked = true;
  if (q.get("critical") === "1") $("onlyCritical").checked = true;
  if (q.get("filter")) $("filterText").value = q.get("filter");
  if (q.get("accept") === "1") setTimeout(loadAcceptance, 50);
})();

loadSim().catch(err => {
  $("kpis").innerHTML = `<div class="kpi bad"><div class="name">加载失败</div><div class="value" style="font-size:15px">${esc(err.message)}</div></div>`;
});
