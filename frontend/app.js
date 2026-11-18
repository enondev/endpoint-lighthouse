const DEFAULT_API_BASE = "https://endpoint-lighthouse.onrender.com";

const state = {
  apiBase: resolveInitialApiBase(),
  endpoints: [],
  incidents: [],
  sseEventsCount: 0,
  eventSource: null,
};

function resolveInitialApiBase() {
  const params = new URLSearchParams(window.location.search);
  const queryApi = params.get("api");
  if (queryApi) {
    const cleaned = queryApi.replace(/\/+$/, "");
    localStorage.setItem("endpoint_lighthouse_api_base", cleaned);
    return cleaned;
  }
  return (localStorage.getItem("endpoint_lighthouse_api_base") || DEFAULT_API_BASE).replace(/\/+$/, "");
}

// DOM References
const els = {
  sseDot: document.getElementById("sse-dot"),
  sseStatus: document.getElementById("sse-status"),
  sseLabel: document.getElementById("sse-label"),
  btnConfig: document.getElementById("btn-config"),
  btnRefresh: document.getElementById("btn-refresh"),
  configBar: document.getElementById("config-bar"),
  apiUrlInput: document.getElementById("api-url-input"),
  btnSaveApi: document.getElementById("btn-save-api"),
  kpiEndpoints: document.getElementById("kpi-endpoints"),
  kpiOpenIncidents: document.getElementById("kpi-open-incidents"),
  kpiBreakingCount: document.getElementById("kpi-breaking-count"),
  kpiGatewayStatus: document.getElementById("kpi-gateway-status"),
  kpiLastSync: document.getElementById("kpi-last-sync"),
  kpiEventsCount: document.getElementById("kpi-events-count"),
  kpiLastPing: document.getElementById("kpi-last-ping"),
  severityFilter: document.getElementById("severity-filter"),
  categoryFilter: document.getElementById("category-filter"),
  incidentsList: document.getElementById("incidents-list"),
  endpointsList: document.getElementById("endpoints-list"),
  modal: document.getElementById("incident-modal"),
  modalSeverity: document.getElementById("modal-severity"),
  modalTitle: document.getElementById("modal-title"),
  modalOldHash: document.getElementById("modal-old-hash"),
  modalNewHash: document.getElementById("modal-new-hash"),
  modalAnalysis: document.getElementById("modal-analysis"),
  modalPatch: document.getElementById("modal-patch"),
  modalDiff: document.getElementById("modal-diff"),
  btnCloseModal: document.getElementById("btn-close-modal"),
  btnCopyPatch: document.getElementById("btn-copy-patch"),
};

// Initialization
function init() {
  els.apiUrlInput.value = state.apiBase;

  els.btnConfig.addEventListener("click", () => {
    els.configBar.classList.toggle("hidden");
  });

  els.btnSaveApi.addEventListener("click", () => {
    const val = els.apiUrlInput.value.trim().replace(/\/+$/, "");
    if (!val) return;
    state.apiBase = val;
    localStorage.setItem("endpoint_lighthouse_api_base", val);
    els.configBar.classList.add("hidden");
    bootstrapData();
    connectSSE();
  });

  els.btnRefresh.addEventListener("click", bootstrapData);
  els.severityFilter.addEventListener("change", renderIncidents);
  els.categoryFilter.addEventListener("change", renderEndpoints);

  els.btnCloseModal.addEventListener("click", () => els.modal.close());
  els.modal.addEventListener("click", (e) => {
    if (e.target === els.modal) els.modal.close();
  });

  els.btnCopyPatch.addEventListener("click", async () => {
    try {
      await navigator.clipboard.writeText(els.modalPatch.textContent || "");
      els.btnCopyPatch.textContent = "Copied!";
      setTimeout(() => (els.btnCopyPatch.textContent = "Copy Snippet"), 1500);
    } catch {
      els.btnCopyPatch.textContent = "Copy failed";
    }
  });

  bootstrapData();
  connectSSE();
}

async function fetchJSON(path) {
  const res = await fetch(`${state.apiBase}${path}`, {
    headers: { Accept: "application/json" },
  });
  if (!res.ok) {
    throw new Error("Gateway request failed");
  }
  return res.json();
}

async function bootstrapData() {
  try {
    const [statusData, endpointsData, incidentsData] = await Promise.all([
      fetchJSON("/api/v1/status").catch(() => ({ status: "ok" })),
      fetchJSON("/api/v1/endpoints"),
      fetchJSON("/api/v1/incidents"),
    ]);

    state.endpoints = Array.isArray(endpointsData) ? endpointsData : endpointsData.endpoints || [];
    state.incidents = Array.isArray(incidentsData) ? incidentsData : incidentsData.incidents || [];

    els.kpiGatewayStatus.textContent = (statusData.status || "HEALTHY").toUpperCase();
    els.kpiGatewayStatus.className = "kpi-value status-ok";
    els.kpiLastSync.textContent = `Synced ${new Date().toLocaleTimeString()}`;

    populateCategoryFilter();
    updateKPIs();
    renderEndpoints();
    renderIncidents();
  } catch {
    // Sanitized error state: never expose raw stack traces, internal URLs, or exception strings
    state.endpoints = [];
    state.incidents = [];

    els.kpiGatewayStatus.textContent = "OFFLINE";
    els.kpiGatewayStatus.className = "kpi-value status-err";
    els.kpiLastSync.textContent =
      window.location.protocol === "file:"
        ? "Serve over HTTP/HTTPS to connect"
        : "Telemetry gateway unavailable";

    updateKPIs();
    renderSanitizedErrorState(
      els.incidentsList,
      "Unable to load drift incidents. Verify the telemetry gateway is reachable and served over HTTP/HTTPS."
    );
    renderSanitizedErrorState(
      els.endpointsList,
      "Unable to load monitored endpoints."
    );
  }
}

function renderSanitizedErrorState(container, message) {
  container.innerHTML = "";
  const box = document.createElement("div");
  box.className = "error-state";
  box.setAttribute("role", "alert");
  box.textContent = message;
  container.appendChild(box);
}

function updateKPIs() {
  els.kpiEndpoints.textContent = String(state.endpoints.length);

  const openIncidents = state.incidents.filter((i) => (i.status || "OPEN") === "OPEN");
  const breakingCount = openIncidents.filter((i) => i.severity === "BREAKING").length;

  els.kpiOpenIncidents.textContent = String(openIncidents.length);
  els.kpiBreakingCount.textContent = `${breakingCount} Breaking`;
}

function populateCategoryFilter() {
  const existing = els.categoryFilter.value;
  const categories = [...new Set(state.endpoints.map((ep) => ep.category).filter(Boolean))].sort();

  els.categoryFilter.innerHTML = `<option value="ALL">All Categories</option>`;
  for (const cat of categories) {
    const opt = document.createElement("option");
    opt.value = cat;
    opt.textContent = cat.toUpperCase();
    els.categoryFilter.appendChild(opt);
  }
  if (categories.includes(existing)) {
    els.categoryFilter.value = existing;
  }
}

function getEndpointName(endpointId) {
  const match = state.endpoints.find((ep) => ep.id === endpointId);
  return match ? match.name : endpointId;
}

function renderIncidents() {
  const filter = els.severityFilter.value;
  const filtered =
    filter === "ALL"
      ? state.incidents
      : state.incidents.filter((inc) => (inc.severity || "PENDING") === filter);

  els.incidentsList.innerHTML = "";
  if (filtered.length === 0) {
    const empty = document.createElement("div");
    empty.className = "empty-state";
    empty.textContent = "No drift incidents match the current filter.";
    els.incidentsList.appendChild(empty);
    return;
  }

  for (const inc of filtered) {
    const severity = (inc.severity || "PENDING").toUpperCase();
    const card = document.createElement("article");
    card.className = "incident-card";
    card.dataset.severity = severity;
    card.setAttribute("role", "button");
    card.tabIndex = 0;

    const top = document.createElement("div");
    top.className = "incident-top";

    const title = document.createElement("span");
    title.className = "incident-title";
    title.textContent = getEndpointName(inc.endpoint_id);

    const badge = document.createElement("span");
    badge.className = `badge ${severity}`;
    badge.textContent = severity;

    top.append(title, badge);

    const summary = document.createElement("p");
    summary.className = "incident-summary";
    summary.textContent =
      inc.ai_analysis ||
      (severity === "PENDING"
        ? "SnapDeploy AI worker analyzing schema diff..."
        : "Click to inspect structural diff and migration patch.");

    const meta = document.createElement("div");
    meta.className = "incident-meta";

    const hashes = document.createElement("span");
    const oldShort = (inc.old_schema_hash || "").slice(0, 8);
    const newShort = (inc.new_schema_hash || "").slice(0, 8);
    hashes.textContent = `${oldShort} → ${newShort}`;

    const ts = document.createElement("span");
    ts.textContent = inc.detected_at || "Just now";

    meta.append(hashes, ts);
    card.append(top, summary, meta);

    card.addEventListener("click", () => openIncidentModal(inc));
    card.addEventListener("keydown", (e) => {
      if (e.key === "Enter" || e.key === " ") {
        e.preventDefault();
        openIncidentModal(inc);
      }
    });

    els.incidentsList.appendChild(card);
  }
}

function renderEndpoints() {
  const catFilter = els.categoryFilter.value;
  const filtered =
    catFilter === "ALL"
      ? state.endpoints
      : state.endpoints.filter((ep) => ep.category === catFilter);

  els.endpointsList.innerHTML = "";
  if (filtered.length === 0) {
    const empty = document.createElement("div");
    empty.className = "empty-state";
    empty.textContent = "No endpoints found.";
    els.endpointsList.appendChild(empty);
    return;
  }

  for (const ep of filtered) {
    const row = document.createElement("div");
    row.className = "endpoint-row";

    const info = document.createElement("div");
    const name = document.createElement("div");
    name.className = "ep-name";
    name.textContent = ep.name;

    const url = document.createElement("div");
    url.className = "ep-url";
    url.textContent = ep.url;

    info.append(name, url);

    const tag = document.createElement("span");
    tag.className = "ep-tag";
    tag.textContent = ep.category || "api";

    row.append(info, tag);
    els.endpointsList.appendChild(row);
  }
}

function openIncidentModal(inc) {
  const severity = (inc.severity || "PENDING").toUpperCase();
  els.modalSeverity.className = `badge ${severity}`;
  els.modalSeverity.textContent = severity;
  els.modalTitle.textContent = `${getEndpointName(inc.endpoint_id)} — Drift Report`;
  els.modalOldHash.textContent = inc.old_schema_hash || "N/A";
  els.modalNewHash.textContent = inc.new_schema_hash || "N/A";
  els.modalAnalysis.textContent =
    inc.ai_analysis || "Awaiting Workers AI analysis from SnapDeploy worker.";
  els.modalPatch.textContent =
    inc.ai_patch_snippet || "// No client patch snippet generated yet.";

  let formattedDiff = inc.diff_summary || "{}";
  if (typeof formattedDiff === "string") {
    try {
      formattedDiff = JSON.stringify(JSON.parse(formattedDiff), null, 2);
    } catch {
      // Keep raw string if not valid JSON
    }
  } else {
    formattedDiff = JSON.stringify(formattedDiff, null, 2);
  }
  els.modalDiff.textContent = formattedDiff;

  els.modal.showModal();
}

function connectSSE() {
  if (state.eventSource) {
    state.eventSource.close();
  }

  setSSEState("connecting", "Connecting to SSE...");
  const es = new EventSource(`${state.apiBase}/api/v1/stream`);
  state.eventSource = es;

  es.onopen = () => {
    setSSEState("live", "Live Stream Connected");
  };

  es.addEventListener("connected", () => {
    setSSEState("live", "Live Stream Connected");
    bumpEventCounter("Connected to Render SSE hub");
  });

  es.addEventListener("ping", () => {
    setSSEState("live", "Live Stream Connected");
    els.kpiLastPing.textContent = `Heartbeat ${new Date().toLocaleTimeString()}`;
  });

  es.addEventListener("drift_detected", (event) => {
    bumpEventCounter("Drift detected!");
    const data = safeParseJSON(event.data);
    if (!data) return;

    upsertIncident({
      id: data.incident_id ?? Date.now(),
      endpoint_id: data.endpoint_id,
      old_schema_hash: data.old_schema_hash,
      new_schema_hash: data.new_schema_hash,
      severity: "PENDING",
      status: "OPEN",
      detected_at: new Date().toISOString(),
    });
  });

  es.addEventListener("analysis_ready", (event) => {
    bumpEventCounter("AI analysis ready");
    const data = safeParseJSON(event.data);
    if (!data) return;

    upsertIncident({
      id: data.incident_id,
      endpoint_id: data.endpoint_id,
      old_schema_hash: data.old_schema_hash,
      new_schema_hash: data.new_schema_hash,
      severity: data.severity || "WARNING",
      diff_summary: data.diff_summary,
      ai_analysis: data.ai_analysis,
      ai_patch_snippet: data.ai_patch_snippet,
      status: "OPEN",
    });
  });

  es.onerror = () => {
    setSSEState("error", "SSE Reconnecting...");
  };
}

function upsertIncident(incoming) {
  const idx = state.incidents.findIndex(
    (item) =>
      (incoming.id && item.id === incoming.id) ||
      (item.endpoint_id === incoming.endpoint_id &&
        item.new_schema_hash === incoming.new_schema_hash)
  );

  if (idx >= 0) {
    state.incidents[idx] = { ...state.incidents[idx], ...incoming };
  } else {
    state.incidents.unshift(incoming);
  }

  updateKPIs();
  renderIncidents();
}

function setSSEState(mode, label) {
  els.sseLabel.textContent = label;
  els.sseDot.className = "logo-dot";
  els.sseStatus.className = "sse-pill";

  if (mode === "live") {
    els.sseDot.classList.add("connected");
    els.sseStatus.classList.add("live");
  } else if (mode === "error") {
    els.sseDot.classList.add("error");
  }
}

function bumpEventCounter(note) {
  state.sseEventsCount += 1;
  els.kpiEventsCount.textContent = String(state.sseEventsCount);
  els.kpiLastPing.textContent = note;
}

function safeParseJSON(raw) {
  try {
    return JSON.parse(raw);
  } catch {
    return null;
  }
}

init();