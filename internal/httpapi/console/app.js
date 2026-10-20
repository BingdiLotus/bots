(() => {
  "use strict";

  const byId = (id) => document.getElementById(id);
  const tenant = byId("console-root").dataset.tenant;
  const base = "/api/tenants/" + encodeURIComponent(tenant) + "/bots";
  const controllers = new Set();
  const state = { token: "", epoch: 0, list: 0, selection: 0, records: 0,
    bots: [], bot: null, kind: "knowledge", mutating: false, loadingBots: false };
  const tabs = Array.from(byId("records-tabs").querySelectorAll("[data-kind]"));

  class RequestError extends Error {
    constructor(message, status = 0, code = "") { super(message); this.status = status; this.code = code; }
  }
  const stale = () => new RequestError("", 0, "stale");
  const ignored = (error) => error.code === "stale" || error.code === "unauthorized";
  const text = (value) => value === null || value === undefined || value === "" ? "—" :
    typeof value === "object" ? JSON.stringify(value) : String(value);
  const node = (tag, value, className) => {
    const element = document.createElement(tag);
    if (value !== undefined) element.textContent = text(value);
    if (className) element.className = className;
    return element;
  };
  const marker = () => ({ epoch: state.epoch, selection: state.selection });
  const current = (version) => version.epoch === state.epoch && version.selection === state.selection && state.token !== "";
  const botPath = (id) => base + "/" + encodeURIComponent(id);

  function notice(message, kind = "") {
    const element = byId("notice");
    element.textContent = message;
    element.className = "notice" + (kind ? " " + kind : "");
    element.hidden = !message;
  }

  function controls() {
    const disabled = !state.bot || state.mutating;
    byId("config-fields").disabled = disabled;
    byId("toggle-bot").disabled = disabled;
    byId("toggle-account").disabled = disabled;
    byId("refresh-records").disabled = !state.bot;
    byId("bot-select").disabled = state.loadingBots || state.mutating || state.bots.length === 0;
    byId("refresh-bots").disabled = state.loadingBots || state.mutating;
    byId("records-body").querySelectorAll("[data-knowledge-action]").forEach((button) => {
      button.disabled = disabled || button.dataset.eligible !== "true";
    });
  }

  function clearBot() {
    state.selection += 1;
    state.records += 1;
    state.bot = null;
    state.mutating = false;
    byId("bot-content").hidden = true;
    byId("config-form").reset();
    ["bot-name", "bot-identity", "bot-status", "config-revision", "records-status", "records-empty", "records-caption"].forEach((id) => { byId(id).textContent = ""; });
    byId("binding-details").replaceChildren();
    byId("records-head").replaceChildren();
    byId("records-body").replaceChildren();
    byId("records-table").setAttribute("aria-busy", "false");
    byId("records-empty").hidden = true;
    controls();
  }

  function clearSession(message = "") {
    state.epoch += 1;
    state.list += 1;
    state.token = "";
    controllers.forEach((controller) => controller.abort());
    controllers.clear();
    state.bots = [];
    state.loadingBots = false;
    clearBot();
    byId("bot-select").replaceChildren();
    byId("scope-filter").value = "";
    byId("workspace").hidden = true;
    byId("logout-button").hidden = true;
    byId("auth-panel").hidden = false;
    byId("login-form").reset();
    byId("login-button").disabled = false;
    byId("login-button").textContent = "登录";
    byId("auth-error").textContent = message;
    byId("auth-error").hidden = !message;
    notice("");
  }

  async function api(path, method = "GET", body) {
    const epoch = state.epoch;
    const token = state.token;
    if (!token) throw stale();
    const controller = new AbortController();
    controllers.add(controller);
    const timeout = window.setTimeout(() => controller.abort(), 20000);
    try {
      let response;
      try {
        response = await fetch(path, { method, credentials: "omit", cache: "no-store", redirect: "error",
          headers: { Authorization: "Bearer " + token, Accept: "application/json",
            ...(body === undefined ? {} : { "Content-Type": "application/json" }) },
          body: body === undefined ? undefined : JSON.stringify(body), signal: controller.signal });
      } catch (_) {
        if (epoch !== state.epoch) throw stale();
        throw new RequestError(method === "GET" ? "无法读取服务，请稍后刷新。" : "操作结果尚未确认，请重新读取状态后核对。", 0, "network");
      }
      if (epoch !== state.epoch) throw stale();
      if (response.status === 401 || response.status === 403) {
        clearSession("访问凭据无效、已失效或无权访问。请重新登录。");
        throw new RequestError("", response.status, "unauthorized");
      }
      let data;
      try { data = await response.json(); }
      catch (_) { throw new RequestError("服务返回了无法读取的响应，请刷新后核对。", response.status, "invalid_response"); }
      if (epoch !== state.epoch) throw stale();
      if (!response.ok) {
        const code = data && data.error && data.error.code;
        const messages = { conflict: "版本已变化。请重新读取后合并修改，当前输入尚未保存。",
          not_found: "记录不存在或不在当前范围。", invalid_request: "请求无效，请检查填写内容。",
          unavailable: "服务暂不可用，请稍后重试。", disabled: "Bot 或账号当前已停用。",
          in_flight_or_unknown: "操作已在途或结果未知，需先核对。" };
        throw new RequestError(messages[code] || "请求未能完成（HTTP " + response.status + "），请刷新后核对。", response.status, code || "request_failed");
      }
      return data;
    } finally {
      window.clearTimeout(timeout);
      controllers.delete(controller);
    }
  }

  function report(error) { if (!ignored(error)) notice(error.message || "操作未完成，请刷新后核对。", "error"); }

  function validateBot(bot, id) {
    if (!bot || bot.id !== id || bot.tenant_id !== tenant || !bot.config || !Number.isInteger(bot.revision) || bot.revision < 1) {
      throw new RequestError("Bot 配置响应不完整，请重新读取。", 0, "invalid_response");
    }
  }

  function summary() {
    const bot = state.bot;
    if (!bot) return;
    byId("bot-name").textContent = bot.name || bot.id;
    byId("bot-identity").textContent = bot.id;
    byId("bot-status").textContent = (bot.enabled ? "Bot 已启用" : "Bot 已暂停") + " · " + (bot.account_enabled ? "账号已启用" : "账号已停用");
    byId("config-revision").textContent = "当前版本 " + bot.revision;
    byId("toggle-bot").textContent = bot.enabled ? "暂停 Bot" : "启用 Bot";
    byId("toggle-account").textContent = bot.account_enabled ? "停用账号" : "启用账号";
    byId("toggle-bot").classList.toggle("danger-button", Boolean(bot.enabled));
    byId("toggle-account").classList.toggle("danger-button", Boolean(bot.account_enabled));
  }

  function fillConfig() {
    const bot = state.bot;
    const cfg = bot.config;
    for (const [id, key] of [["instructions", "instructions"], ["model", "model"], ["timezone", "timezone"],
      ["max-output-tokens", "max_output_tokens"], ["max-steps", "max_steps"], ["max-pending-tasks", "max_pending_tasks"]]) {
      byId(id).value = cfg[key] === undefined ? "" : String(cfg[key]);
    }
    byId("max-duration-seconds").value = cfg.max_duration_ms === undefined ? "" : String(cfg.max_duration_ms / 1000);
    const details = byId("binding-details");
    details.replaceChildren();
    for (const [label, value] of [["渠道", cfg.channel_kind], ["安装账号", cfg.installation_id], ["工作区", cfg.workspace_id],
      ["频道", cfg.channel_id], ["Bot 用户", cfg.bot_user_id], ["token 引用", cfg.token_env], ["签名引用", cfg.signing_secret_env],
      ["事件接收", cfg.event_transport || "webhook"], ["App token 引用", cfg.app_token_env],
      ["工具桥接", cfg.bridge_ref], ["回调绑定", cfg.timer_target_ref], ["运行时 Agent", bot.runtime_agent_id || "待绑定"]]) {
      details.append(node("dt", label), node("dd", value));
    }
    summary();
    controls();
  }

  async function loadBot(id) {
    clearBot();
    notice("");
    const version = marker();
    byId("bot-select").value = id;
    try {
      const bot = await api(botPath(id));
      if (!current(version)) return;
      validateBot(bot, id);
      state.bot = bot;
      fillConfig();
      byId("bot-content").hidden = false;
      await loadRecords();
    } catch (error) { if (current(version)) report(error); }
  }

  async function loadBots(preferred) {
    const sequence = ++state.list;
    const epoch = state.epoch;
    clearBot();
    state.bots = [];
    state.loadingBots = true;
    byId("bot-select").replaceChildren();
    byId("empty-bots").hidden = true;
    controls();
    try {
      const data = await api(base);
      if (epoch !== state.epoch || sequence !== state.list) throw stale();
      if (!data || !Array.isArray(data.bots) || data.bots.some((bot) => !bot || typeof bot.id !== "string" || bot.id === "")) {
        throw new RequestError("Bot 列表响应不完整，请刷新后核对。");
      }
      state.bots = data.bots;
      data.bots.forEach((bot) => {
        const option = node("option", (bot.name || bot.id) + " · " + bot.id);
        option.value = bot.id;
        byId("bot-select").append(option);
      });
      byId("auth-panel").hidden = true;
      byId("workspace").hidden = false;
      byId("logout-button").hidden = false;
      byId("empty-bots").hidden = data.bots.length !== 0;
      if (data.bots.length) await loadBot(data.bots.some((bot) => bot.id === preferred) ? preferred : data.bots[0].id);
    } finally {
      if (epoch === state.epoch && sequence === state.list) { state.loadingBots = false; controls(); }
    }
  }

  function date(value) {
    if (!value) return "—";
    const parsed = new Date(value);
    if (Number.isNaN(parsed.getTime())) return text(value);
    try { return new Intl.DateTimeFormat("zh-CN", { dateStyle: "medium", timeStyle: "short", timeZone: state.bot.config.timezone }).format(parsed); }
    catch (_) { return parsed.toLocaleString("zh-CN"); }
  }

  function metadata(cell, label, value) {
    if (value !== undefined && value !== null && value !== "") cell.append(node("span", label + " " + text(value), "cell-secondary mono"));
  }

  function content(cell, value, label = "查看全文") {
    const valueText = text(value);
    cell.append(node("div", valueText.length > 130 ? valueText.slice(0, 130) + "…" : valueText, "cell-content"));
    if (valueText.length > 130) {
      const details = node("details", undefined, "cell-details");
      details.append(node("summary", label), node("pre", valueText));
      cell.append(details);
    }
  }

  const labels = { pending: "待发送", pending_schedule: "待提交计时", scheduled: "已安排", due: "等待发送",
    dispatching: "发送中", confirmed: "平台已接受", completed: "已完成", unknown: "结果未知 · 待核对",
    rejected: "已拒绝", retryable: "等待重试", blocked: "已阻塞", canceled: "已取消", obsolete: "已失效" };
  function badge(cell, value, active) {
    const label = active === undefined ? labels[value] || text(value) : active ? "有效" : "已停用";
    const tone = active === true || ["confirmed", "completed"].includes(value) ? "positive" :
      ["unknown", "retryable", "dispatching"].includes(value) ? "warning" : ["rejected", "blocked"].includes(value) ? "negative" : "";
    cell.append(node("span", label, "state " + tone));
  }

  const tables = {
    knowledge: { title: "知识与经验", empty: "当前范围暂无知识或经验。", columns: [
      ["内容", (cell, row) => { content(cell, row.content); metadata(cell, "类型", row.kind === "experience" ? "经验" : "知识"); metadata(cell, "ID", row.id); }],
      ["来源与范围", (cell, row) => { cell.append(node("span", row.source_id, "mono")); metadata(cell, "范围", row.scope_id); }],
      ["状态 / 版本", (cell, row) => { badge(cell, "", row.active); metadata(cell, "版本", row.revision); }],
      ["创建时间", (cell, row) => cell.append(node("span", date(row.created_at)))],
      ["操作", (cell, row) => {
        const button = node("button", row.active ? "停用" : "已停用", "link-button");
        button.type = "button";
        button.dataset.knowledgeAction = "deactivate";
        button.dataset.eligible = String(Boolean(row.active && Number.isInteger(row.revision) && row.revision > 0));
        button.addEventListener("click", () => deactivate(row));
        cell.append(button);
      }]
    ]},
    tasks: { title: "原生任务", empty: "当前范围暂无原生任务。", columns: [
      ["任务", (cell, row) => { content(cell, row.text); metadata(cell, "ID", row.id); metadata(cell, "范围", row.scope_id); }],
      ["时间 / 状态", (cell, row) => { cell.append(node("div", date(row.due_at))); badge(cell, row.status); metadata(cell, "版本", row.revision); }],
      ["执行关联", (cell, row) => { metadata(cell, "timer", row.timer_id); metadata(cell, "fire", row.fire_id); metadata(cell, "delivery", row.delivery_id); }]
    ]},
    deliveries: { title: "发送回执", empty: "当前范围暂无发送记录。", columns: [
      ["消息与目标", (cell, row) => { content(cell, row.text); metadata(cell, "频道", row.target && row.target.channel_id); metadata(cell, "线程", row.target && row.target.thread_id); metadata(cell, "范围", row.scope_id); }],
      ["结果", (cell, row) => { badge(cell, row.state); metadata(cell, "尝试次数", row.attempts); metadata(cell, "原因", row.error); if (row.state === "retryable") metadata(cell, "下次尝试", date(row.next_attempt_at)); }],
      ["回执与关联", (cell, row) => { metadata(cell, "平台消息", row.provider_message_id); metadata(cell, "operation", row.operation_id); metadata(cell, "任务", row.task_id); metadata(cell, "ID", row.id); }],
      ["创建时间", (cell, row) => cell.append(node("span", date(row.created_at)))]
    ]},
    audit: { title: "治理与执行审计", empty: "当前范围暂无审计记录。", columns: [
      ["时间 / 操作者", (cell, row) => { cell.append(node("div", date(row.created_at))); metadata(cell, "操作者", row.actor); }],
      ["事件与引用", (cell, row) => { cell.append(node("span", row.kind)); metadata(cell, "引用", row.ref_id); metadata(cell, "版本", row.revision); metadata(cell, "范围", row.scope_id); }],
      ["元数据", (cell, row) => { const details = node("details", undefined, "cell-details"); details.append(node("summary", "查看元数据"), node("pre", JSON.stringify(row.data || {}, null, 2))); cell.append(details); }]
    ]}
  };

  async function loadRecords() {
    if (!state.bot) return;
    const version = marker();
    const sequence = ++state.records;
    const kind = state.kind;
    const spec = tables[kind];
    const id = state.bot.id;
    const query = new URLSearchParams({ limit: "50" });
    const scope = byId("scope-filter").value.trim();
    if (scope) query.set("scope_id", scope);
    byId("records-body").replaceChildren();
    byId("records-head").replaceChildren();
    byId("records-empty").hidden = true;
    byId("records-status").textContent = "正在读取…";
    byId("records-table").setAttribute("aria-busy", "true");
    const head = node("tr");
    spec.columns.forEach(([label]) => { const cell = node("th", label); cell.scope = "col"; head.append(cell); });
    byId("records-head").append(head);
    byId("records-caption").textContent = spec.title;
    try {
      const data = await api(botPath(id) + "/" + kind + "?" + query.toString());
      if (!current(version) || sequence !== state.records) return;
      const rows = data && data[kind];
      if (!Array.isArray(rows)) throw new RequestError("记录响应不完整，请刷新后核对。");
      const fragment = document.createDocumentFragment();
      rows.forEach((row) => {
        const tr = node("tr");
        spec.columns.forEach(([, render]) => { const cell = node("td"); render(cell, row); tr.append(cell); });
        fragment.append(tr);
      });
      byId("records-body").replaceChildren(fragment);
      byId("records-status").textContent = "已显示 " + rows.length + " 条（最多 50 条） · 时区 " + text(state.bot.config.timezone);
      byId("records-empty").textContent = spec.empty;
      byId("records-empty").hidden = rows.length !== 0;
      controls();
    } catch (error) {
      if (current(version) && sequence === state.records) { byId("records-status").textContent = "读取失败，请刷新重试。"; report(error); }
    } finally {
      if (current(version) && sequence === state.records) byId("records-table").setAttribute("aria-busy", "false");
    }
  }

  async function mutate(work) {
    if (!state.bot || state.mutating) return;
    const version = marker();
    state.mutating = true;
    notice("");
    controls();
    try { await work(version); }
    catch (error) { if (current(version)) report(error); }
    finally { if (current(version)) { state.mutating = false; controls(); } }
  }

  async function deactivate(row) {
    if (!row.active) return;
    await mutate(async (version) => {
      const query = new URLSearchParams();
      if (row.scope_id) query.set("scope_id", row.scope_id);
      await api(botPath(state.bot.id) + "/knowledge/" + encodeURIComponent(row.id) + "/deactivate?" + query.toString(), "POST", { expected_revision: row.revision });
      if (!current(version)) return;
      notice("知识已停用；相关历史记录仍保留。", "success");
      await loadRecords();
    });
  }

  byId("login-form").addEventListener("submit", async (event) => {
    event.preventDefault();
    const token = byId("admin-token").value.trim();
    byId("admin-token").value = "";
    clearSession();
    if (!token) { byId("auth-error").textContent = "请输入管理员访问凭据。"; byId("auth-error").hidden = false; return; }
    state.token = token;
    const epoch = state.epoch;
    byId("login-button").disabled = true;
    byId("login-button").textContent = "正在登录…";
    try { await loadBots(); }
    catch (error) { if (epoch === state.epoch && !ignored(error)) clearSession(error.message || "登录未能完成，请重试。"); }
    finally { if (epoch === state.epoch) { byId("login-button").disabled = false; byId("login-button").textContent = "登录"; } }
  });
  byId("logout-button").addEventListener("click", () => clearSession());
  window.addEventListener("pagehide", () => clearSession());
  byId("refresh-bots").addEventListener("click", async () => {
    const preferred = state.bot && state.bot.id;
    try { await loadBots(preferred); } catch (error) { report(error); }
  });
  byId("bot-select").addEventListener("change", () => loadBot(byId("bot-select").value));
  byId("reload-config").addEventListener("click", () => { if (state.bot) loadBot(state.bot.id); });

  byId("config-form").addEventListener("submit", (event) => {
    event.preventDefault();
    if (!byId("config-form").reportValidity()) return;
    mutate(async (version) => {
      const bot = state.bot;
      const positive = (id, multiplier = 1) => {
        const value = Math.round(Number(byId(id).value) * multiplier);
        if (!Number.isSafeInteger(value) || value < 1) throw new RequestError("执行限额必须是可表示的正数。");
        return value;
      };
      const cfg = { ...bot.config, instructions: byId("instructions").value.trim(), model: byId("model").value.trim(), timezone: byId("timezone").value.trim(),
        max_output_tokens: positive("max-output-tokens"), max_steps: positive("max-steps"),
        max_duration_ms: positive("max-duration-seconds", 1000), max_pending_tasks: positive("max-pending-tasks") };
      if (!cfg.instructions || !cfg.model || !cfg.timezone) throw new RequestError("人格与 SOP、模型和时区不能为空。");
      const updated = await api(botPath(bot.id) + "/config", "PUT", { expected_revision: bot.revision, config: cfg });
      if (!current(version)) return;
      validateBot(updated, bot.id);
      state.bot = updated;
      fillConfig();
      notice("配置已保存为版本 " + updated.revision + "，新会话使用新配置。", "success");
    });
  });

  for (const [id, endpoint, field] of [["toggle-bot", "enabled", "enabled"], ["toggle-account", "account-enabled", "account_enabled"]]) {
    byId(id).addEventListener("click", () => mutate(async (version) => {
      const enabled = !state.bot[field];
      const result = await api(botPath(state.bot.id) + "/" + endpoint, "PUT", { enabled });
      if (!current(version)) return;
      if (typeof result.enabled !== "boolean") throw new RequestError("启停结果无法读取，请重新读取状态。");
      state.bot[field] = result.enabled;
      summary();
      notice("启停状态已更新。", "success");
    }));
  }

  function chooseTab(button) {
    if (!tables[button.dataset.kind]) return;
    state.kind = button.dataset.kind;
    tabs.forEach((tab) => { const selected = tab === button; tab.setAttribute("aria-selected", String(selected)); tab.tabIndex = selected ? 0 : -1; });
    byId("records-panel").setAttribute("aria-labelledby", button.id);
    notice("");
    loadRecords();
  }
  tabs.forEach((button, index) => {
    button.addEventListener("click", () => chooseTab(button));
    button.addEventListener("keydown", (event) => {
      let next;
      if (event.key === "ArrowRight") next = (index + 1) % tabs.length;
      if (event.key === "ArrowLeft") next = (index + tabs.length - 1) % tabs.length;
      if (event.key === "Home") next = 0;
      if (event.key === "End") next = tabs.length - 1;
      if (next !== undefined) { event.preventDefault(); tabs[next].focus(); chooseTab(tabs[next]); }
    });
  });
  byId("records-filter-form").addEventListener("submit", (event) => { event.preventDefault(); notice(""); loadRecords(); });
  controls();
})();
