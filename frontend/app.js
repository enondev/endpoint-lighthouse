const DEFAULT_API_BASE = "https://endpoint-lighthouse.onrender.com";

const state = {
  apiBase: resolveInitialApiBase(),
  endpoints: [],
  incidents: [],
  sseEventsCount: 0,
  eventSource: null,
  ssePaused: false,
  searchTerm: "",
  statusFilter: "ALL",
  severityFilter: "ALL",
  dateFrom: "",
  dateTo: "",
  page: 1,
};

function loadFilterPreferences() {
  try {
    const raw = localStorage.getItem("endpoint_lighthouse_filters");
    if (raw) {
      const prefs = JSON.parse(raw);
      if (prefs.searchTerm != null) state.searchTerm = prefs.searchTerm;
      if (prefs.statusFilter != null) state.statusFilter = prefs.statusFilter;
      if (prefs.dateFrom != null) state.dateFrom = prefs.dateFrom;
      if (prefs.dateTo != null) state.dateTo = prefs.dateTo;
      if (prefs.severityFilter != null) {
        state.severityFilter = prefs.severityFilter;
      }
      if (prefs.page != null) state.page = prefs.page;
    }
  } catch {
    // Ignore malformed preferences
  }
}

function saveFilterPreferences() {
  try {
    const prefs = {
      searchTerm: state.searchTerm,
      statusFilter: state.statusFilter,
      dateFrom: state.dateFrom,
      dateTo: state.dateTo,
      severityFilter: els.severityFilter ? els.severityFilter.value : "ALL",
      page: state.page,
    };
    localStorage.setItem("endpoint_lighthouse_filters", JSON.stringify(prefs));
  } catch {
    // Ignore quota errors
  }
}

function applyFilterPreferencesToDOM() {
  if (els.incidentSearch) els.incidentSearch.value = state.searchTerm;
  if (els.dateFrom) els.dateFrom.value = state.dateFrom;
  if (els.dateTo) els.dateTo.value = state.dateTo;
  if (els.statusFilter) els.statusFilter.value = state.statusFilter;
  if (els.severityFilter && state.severityFilter) {
    els.severityFilter.value = state.severityFilter;
  }
}

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
  kpiAnalyzingCount: document.getElementById("kpi-analyzing-count"),
  kpiGatewayStatus: document.getElementById("kpi-gateway-status"),
  kpiGatewayCard: document.getElementById("kpi-gateway-card"),
  kpiGatewayDetail: document.getElementById("kpi-gateway-detail"),
  kpiLastSync: document.getElementById("kpi-last-sync"),
  kpiEventsCount: document.getElementById("kpi-events-count"),
  kpiLastPing: document.getElementById("kpi-last-ping"),
  kpiSevBreaking: document.getElementById("kpi-sev-breaking"),
  kpiSevWarning: document.getElementById("kpi-sev-warning"),
  kpiSevNonbreaking: document.getElementById("kpi-sev-nonbreaking"),
  kpiSevPending: document.getElementById("kpi-sev-pending"),
  incidentSearch: document.getElementById("incident-search"),
  dateFrom: document.getElementById("date-from"),
  dateTo: document.getElementById("date-to"),
  statusFilter: document.getElementById("status-filter"),
  severityFilter: document.getElementById("severity-filter"),
  categoryFilter: document.getElementById("category-filter"),
  btnSsePause: document.getElementById("btn-sse-pause"),
  btnExport: document.getElementById("btn-export"),
  incidentsList: document.getElementById("incidents-list"),
  endpointsList: document.getElementById("endpoints-list"),
  paginationBar: document.getElementById("pagination-bar"),
  paginationPage: document.getElementById("pagination-page"),
  btnPrevPage: document.getElementById("btn-prev-page"),
  btnNextPage: document.getElementById("btn-next-page"),
  modal: document.getElementById("incident-modal"),
  modalSeverity: document.getElementById("modal-severity"),
  modalStatusChip: document.getElementById("modal-status-chip"),
  modalTitle: document.getElementById("modal-title"),
  modalOldHash: document.getElementById("modal-old-hash"),
  modalNewHash: document.getElementById("modal-new-hash"),
  modalAnalysis: document.getElementById("modal-analysis"),
  modalAnalysisStatus: document.getElementById("modal-analysis-status"),
  modalPatch: document.getElementById("modal-patch"),
  modalDiff: document.getElementById("modal-diff"),
  modalMetaId: document.getElementById("modal-meta-id"),
  modalMetaEndpointId: document.getElementById("modal-meta-endpoint-id"),
  modalMetaScore: document.getElementById("modal-meta-score"),
  modalMetaDetected: document.getElementById("modal-meta-detected"),
  modalMetaStatus: document.getElementById("modal-meta-status"),
  btnCloseModal: document.getElementById("btn-close-modal"),
  btnCopyPatch: document.getElementById("btn-copy-patch"),
  btnAcknowledge: document.getElementById("btn-acknowledge"),
  btnResolve: document.getElementById("btn-resolve"),
  btnReopen: document.getElementById("btn-reopen"),
};

// Initialization
function init() {
  loadFilterPreferences();
  applyFilterPreferencesToDOM();

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

  els.btnRefresh.addEventListener("click", () => bootstrapData(state.dateFrom, state.dateTo, state.statusFilter, state.page));
  els.btnSsePause.addEventListener("click", toggleSsePause);
  els.btnExport.addEventListener("click", exportIncidents);
  els.btnPrevPage.addEventListener("click", () => {
    if (state.page > 1) {
      state.page--;
      bootstrapData(state.dateFrom, state.dateTo, state.statusFilter, state.page);
    }
  });
  els.btnNextPage.addEventListener("click", () => {
    state.page++;
    bootstrapData(state.dateFrom, state.dateTo, state.statusFilter, state.page);
  });
  els.incidentSearch.addEventListener("input", (e) => {
    state.searchTerm = e.target.value.toLowerCase();
    saveFilterPreferences();
    renderIncidents();
  });
  els.dateFrom.addEventListener("change", (e) => {
    state.dateFrom = e.target.value;
    state.page = 1;
    saveFilterPreferences();
    bootstrapData(state.dateFrom, state.dateTo, state.statusFilter, state.page);
  });
  els.dateTo.addEventListener("change", (e) => {
    state.dateTo = e.target.value;
    state.page = 1;
    saveFilterPreferences();
    bootstrapData(state.dateFrom, state.dateTo, state.statusFilter, state.page);
  });
  els.statusFilter.addEventListener("change", (e) => {
    state.statusFilter = e.target.value;
    state.page = 1;
    saveFilterPreferences();
    bootstrapData(state.dateFrom, state.dateTo, state.statusFilter, state.page);
  });
  els.severityFilter.addEventListener("change", () => {
    saveFilterPreferences();
    renderIncidents();
  });
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

  // Incident status transitions
  els.btnAcknowledge.addEventListener("click", async () => {
    await transitionIncident("ACKNOWLEDGED");
  });
  els.btnResolve.addEventListener("click", async () => {
    await transitionIncident("RESOLVED");
  });
  els.btnReopen.addEventListener("click", async () => {
    await transitionIncident("OPEN");
  });

  bootstrapData(state.dateFrom, state.dateTo, state.statusFilter, state.page);
  connectSSE();
}

function showToast(message, type = "info") {
  const container = document.getElementById("toast-container");
  if (!container) return;

  const toast = document.createElement("div");
  toast.className = `toast ${type}`;

  let iconHtml = "";
  if (type === "success") {
    iconHtml = "<svg width='18' height='18' viewBox='0 0 24 24' fill='none' stroke='currentColor' stroke-width='2'><path d='M22 11.08V12a10 10 0 1 1-5.93-9.14'></path><polyline points='22 4 12 14.01 9 11.01'></polyline></svg>";
  } else if (type === "error") {
    iconHtml = "<svg width='18' height='18' viewBox='0 0 24 24' fill='none' stroke='currentColor' stroke-width='2'><circle cx='12' cy='12' r='10'></circle><path d='M15 9l6 6M9 9l6 6'></path></svg>";
  } else {
    iconHtml = "<svg width='18' height='18' viewBox='0 0 24 24' fill='none' stroke='currentColor' stroke-width='2'><circle cx='12' cy='12' r='10'></circle><path d='M12 16v-4'></path><path d='M12 8h.01'></path></svg>";
  }

  toast.innerHTML = `
    <div class="toast-icon">${iconHtml}</div>
    <div class="toast-message">${escapeHtml(message)}</div>
  `;

  container.appendChild(toast);

  // Auto-remove after 3 seconds
  setTimeout(() => {
    toast.style.animation = "toastFadeOut 0.3s ease-in forwards";
    setTimeout(() => toast.remove(), 300);
  }, 3000);
}

function escapeHtml(text) {
  const div = document.createElement("div");
  div.textContent = text;
  return div.innerHTML;
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

async function patchIncidentStatus(incidentId, newStatus) {
  const res = await fetch(`${state.apiBase}/api/v1/incidents/${incidentId}/status`, {
    method: "PATCH",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ status: newStatus }),
  });
  if (!res.ok) {
    const err = await res.json().catch(() => ({}));
    throw new Error(err.error || "Status update failed");
  }
  return res.json();
}

async function transitionIncident(newStatus) {
  const incId = els.modalMetaId.textContent;
  if (!incId || incId === "—") return;

  const oldStatus = els.modalMetaStatus.textContent;

  // Determine which button was clicked to disable it during request
  const btnMap = {
    ACKNOWLEDGED: els.btnAcknowledge,
    RESOLVED: els.btnResolve,
    OPEN: els.btnReopen,
  };
  const activeBtn = btnMap[newStatus];
  if (activeBtn) {
    activeBtn.disabled = true;
    activeBtn.textContent = "Updating…";
  }

  try {
    await patchIncidentStatus(incId, newStatus);

    // Show toast on success
    const statusLabels = {
      ACKNOWLEDGED: "Acknowledged",
      RESOLVED: "Resolved",
      OPEN: "Reopened",
    };
    showToast(`Incident #${incId} ${statusLabels[newStatus] || newStatus}`, "success");

    // Update local state so the list reflects the change immediately
    const idx = state.incidents.findIndex((i) => String(i.id) === incId);
    if (idx >= 0) {
      state.incidents[idx].status = newStatus;
    }

    // Update modal chips
    els.modalStatusChip.textContent = newStatus;
    els.modalStatusChip.className = `status-chip ${newStatus.toLowerCase()}`;
    els.modalMetaStatus.textContent = newStatus;

    // Re-render incident list so card status chip updates
    renderIncidents();
    updateKPIs();

    // Close modal after a brief moment so user sees the change
    setTimeout(() => els.modal.close(), 800);
  } catch (err) {
    showToast(`Failed to update status: ${err.message}`, "error");
  } finally {
    // Re-enable button
    if (activeBtn) {
      activeBtn.disabled = false;
      activeBtn.textContent =
        activeBtn.id === "btn-acknowledge"
          ? "Acknowledge"
          : activeBtn.id === "btn-resolve"
          ? "Resolve"
          : "Reopen";
    }
  }
}

async function bootstrapData(from, to, status, page) {
  const incidentParams = new URLSearchParams();
  if (from) incidentParams.set("from", from + "T00:00:00Z");
  if (to) incidentParams.set("to", to + "T23:59:59Z");
  if (status && status !== "ALL") incidentParams.set("status", status);
  if (page && page > 1) incidentParams.set("page", String(page));
  const incidentsPath = "/api/v1/incidents" + (incidentParams.toString() ? "?" + incidentParams.toString() : "");

  try {
    const [statusData, endpointsData, incidentsData] = await Promise.all([
      fetchJSON("/api/v1/status").catch(() => ({ status: "ok" })),
      fetchJSON("/api/v1/endpoints"),
      fetchJSON(incidentsPath),
    ]);

    state.endpoints = Array.isArray(endpointsData) ? endpointsData : endpointsData.endpoints || [];
    if (incidentsData && incidentsData.incidents) {
      state.incidents = incidentsData.incidents;
      state.totalCount = incidentsData.total_count || 0;
      state.page = incidentsData.page || 1;
      state.limit = incidentsData.limit || 25;
    } else {
      state.incidents = [];
      state.totalCount = 0;
      state.page = 1;
      state.limit = 25;
    }

    els.kpiGatewayStatus.textContent = (statusData.status || "HEALTHY").toUpperCase();
    els.kpiGatewayStatus.className =
      statusData.status === "drift_detected" ? "kpi-value status-warn" : "kpi-value status-ok";
    els.kpiLastSync.textContent = `Synced ${new Date().toLocaleTimeString()}`;

    populateCategoryFilter();
    updateKPIs();
    renderEndpoints();
    renderIncidents();
  } catch {
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

function relativeTime(iso) {
  if (!iso) return "—";
  try {
    const d = new Date(iso);
    const now = new Date();
    const secs = Math.floor((now - d) / 1000);
    if (secs < 60) return "just now";
    const mins = Math.floor(secs / 60);
    if (mins < 60) return `${mins}m ago`;
    const hours = Math.floor(mins / 60);
    if (hours < 24) return `${hours}h ago`;
    const days = Math.floor(hours / 24);
    return `${days}d ago`;
  } catch {
    return iso;
  }
}

function updateKPIs() {
  els.kpiEndpoints.textContent = String(state.endpoints.length);

  const openIncidents = state.incidents.filter((i) => (i.status || "OPEN") === "OPEN");
  const breakingCount = openIncidents.filter((i) => i.severity === "BREAKING").length;
  const analyzingCount = state.incidents.filter((i) => i.severity === "PENDING").length;

  els.kpiOpenIncidents.textContent = String(openIncidents.length);
  els.kpiBreakingCount.textContent = `${breakingCount} Breaking`;
  els.kpiAnalyzingCount.textContent = String(analyzingCount);

  // Latest incident for gateway KPI
  const latestInc = state.incidents.length > 0 ? state.incidents[0] : null;
  if (latestInc) {
    const sev = (latestInc.severity || "PENDING").toUpperCase();
    els.kpiGatewayStatus.textContent = sev;
    els.kpiGatewayStatus.className = `kpi-value status-${sev.toLowerCase()}`;
    els.kpiGatewayDetail.textContent = `${getEndpointName(latestInc.endpoint_id)} — ${sev}`;
    els.kpiGatewayCard.onclick = () => openIncidentModal(latestInc);
    els.kpiGatewayCard.style.cursor = "pointer";
  } else {
    els.kpiGatewayStatus.textContent = "OK";
    els.kpiGatewayStatus.className = "kpi-value status-ok";
    els.kpiGatewayDetail.textContent = "No recent incidents";
    els.kpiGatewayCard.onclick = null;
    els.kpiGatewayCard.style.cursor = "default";
  }

  // Severity distribution
  const sevCounts = { BREAKING: 0, WARNING: 0, NON_BREAKING: 0, PENDING: 0 };
  for (const inc of state.incidents) {
    const sev = (inc.severity || "PENDING").toUpperCase();
    if (sevCounts[sev] !== undefined) sevCounts[sev]++;
  }
  els.kpiSevBreaking.textContent = String(sevCounts.BREAKING);
  els.kpiSevWarning.textContent = String(sevCounts.WARNING);
  els.kpiSevNonbreaking.textContent = String(sevCounts.NON_BREAKING);
  els.kpiSevPending.textContent = String(sevCounts.PENDING);
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

function matchesFilters(inc) {
  const search = state.searchTerm;
  const name = (inc.endpoint_name || getEndpointName(inc.endpoint_id) || "").toLowerCase();
  const endpointId = (inc.endpoint_id || "").toLowerCase();
  if (search && !name.includes(search) && !endpointId.includes(search)) {
    return false;
  }

  const status = state.statusFilter;
  const incStatus = (inc.status || "OPEN").toUpperCase();
  if (status !== "ALL" && incStatus !== status) {
    return false;
  }

  const sevFilter = els.severityFilter.value;
  const incSev = (inc.severity || "PENDING").toUpperCase();
  if (sevFilter !== "ALL" && incSev !== sevFilter) {
    return false;
  }

  // Date range filter (inclusive) — needed for SSE events that bypass backend filtering
  const detected = inc.detected_at || "";
  if (state.dateFrom && detected < state.dateFrom + "T00:00:00Z") {
    return false;
  }
  if (state.dateTo && detected > state.dateTo + "T23:59:59Z") {
    return false;
  }

  return true;
}

function renderIncidents() {
  const filtered = state.incidents
    .filter(matchesFilters)
    .sort((a, b) => {
      // Prioritize by server-assigned severity_score (highest first), then by most recent detected_at
      const scoreA = Number(a.severity_score || 0);
      const scoreB = Number(b.severity_score || 0);
      if (scoreA !== scoreB) return scoreB - scoreA;
      const dateA = new Date(a.detected_at || 0).getTime();
      const dateB = new Date(b.detected_at || 0).getTime();
      return dateB - dateA;
    });

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
    const status = (inc.status || "OPEN").toUpperCase();
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
    if (severity === "PENDING") {
      summary.textContent = "AI worker analyzing schema diff&hellip;";
    } else {
      summary.textContent =
        inc.ai_analysis ||
        "Click to inspect structural diff and migration patch.";
    }

    const meta = document.createElement("div");
    meta.className = "incident-meta";

    const hashes = document.createElement("span");
    const oldShort = (inc.old_schema_hash || "").slice(0, 8);
    const newShort = (inc.new_schema_hash || "").slice(0, 8);
    hashes.textContent = `${oldShort} → ${newShort}`;

    const ts = document.createElement("span");
    ts.textContent = relativeTime(inc.detected_at);

    const statusChip = document.createElement("span");
    statusChip.className = `status-chip ${status.toLowerCase()}`;
    statusChip.textContent = status;

    meta.append(hashes, ts, statusChip);
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

  // Pagination controls
  const totalPages = Math.max(1, Math.ceil(state.totalCount / state.limit));
  const hasMore = state.page < totalPages;
  els.paginationBar.style.display = state.page > 1 || hasMore ? "flex" : "none";
  els.paginationPage.textContent = `Page ${state.page} of ${totalPages}`;
  els.btnPrevPage.disabled = state.page <= 1;
  els.btnNextPage.disabled = state.page >= totalPages;
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
  const status = (inc.status || "OPEN").toUpperCase();

  els.modalSeverity.className = `badge ${severity}`;
  els.modalSeverity.textContent = severity;

  els.modalStatusChip.style.display = severity === "PENDING" ? "inline" : "none";
  els.modalStatusChip.className = `status-chip ${status.toLowerCase()}`;
  els.modalStatusChip.textContent = status;

  els.modalTitle.textContent = `${getEndpointName(inc.endpoint_id)} — Drift Report`;
  els.modalOldHash.textContent = inc.old_schema_hash || "N/A";
  els.modalNewHash.textContent = inc.new_schema_hash || "N/A";

  const isAnalyzing = severity === "PENDING";
  els.modalAnalysisStatus.style.display = isAnalyzing ? "inline" : "none";
  els.modalAnalysisStatus.textContent = isAnalyzing ? "Analyzing" : "Analyzed";
  els.modalAnalysisStatus.className = `badge ${isAnalyzing ? "pending" : "ok"}`;

  els.modalAnalysis.textContent =
    isAnalyzing || !inc.ai_analysis
      ? "AI worker analyzing schema diff&hellip;"
      : inc.ai_analysis;
  els.modalPatch.textContent =
    inc.ai_patch_snippet || "// No client patch snippet generated yet.";

  els.modalMetaId.textContent = inc.id || "—";
  els.modalMetaEndpointId.textContent = inc.endpoint_id || "—";
  els.modalMetaScore.textContent = String(inc.severity_score || "—");
  els.modalMetaDetected.textContent = relativeTime(inc.detected_at);
  els.modalMetaStatus.textContent = status;

  // Show/hide status action buttons based on current status
  const isOpen = status === "OPEN";
  const isAcknowledged = status === "ACKNOWLEDGED";
  const isResolved = status === "RESOLVED";
  els.btnAcknowledge.style.display = isOpen ? "inline-block" : "none";
  els.btnResolve.style.display = (isOpen || isAcknowledged) ? "inline-block" : "none";
  els.btnReopen.style.display = (isAcknowledged || isResolved) ? "inline-block" : "none";

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

  if (state.ssePaused) {
    setSSEState("connecting", "Live stream paused");
    els.btnSsePause.textContent = "&#9658;";
    els.btnSsePause.title = "Resume live event stream";
    return;
  }

  setSSEState("connecting", "Connecting to SSE&hellip;");
  const es = new EventSource(`${state.apiBase}/api/v1/stream`);
  state.eventSource = es;

  es.onopen = () => {
    if (state.ssePaused) return;
    setSSEState("live", "Live Stream Connected");
  };

  es.addEventListener("connected", () => {
    if (state.ssePaused) return;
    setSSEState("live", "Live Stream Connected");
    bumpEventCounter("Connected to Render SSE hub");
  });

  es.addEventListener("ping", () => {
    if (state.ssePaused) return;
    setSSEState("live", "Live Stream Connected");
    els.kpiLastPing.textContent = `Heartbeat ${new Date().toLocaleTimeString()}`;
  });

  es.addEventListener("drift_detected", (event) => {
    if (state.ssePaused) return;
    bumpEventCounter("Drift detected!");
    const data = safeParseJSON(event.data);
    if (!data) return;

    upsertIncident({
      id: data.incident_id ?? Date.now(),
      endpoint_id: data.endpoint_id,
      endpoint_name: data.endpoint_name,
      old_schema_hash: data.old_schema_hash,
      new_schema_hash: data.new_schema_hash,
      severity: "PENDING",
      status: "OPEN",
      detected_at: new Date().toISOString(),
    });
  });

  es.addEventListener("analysis_ready", (event) => {
    if (state.ssePaused) return;
    bumpEventCounter("AI analysis ready");
    const data = safeParseJSON(event.data);
    if (!data) return;

    upsertIncident({
      id: data.incident_id,
      endpoint_id: data.endpoint_id,
      endpoint_name: data.endpoint_name,
      old_schema_hash: data.old_schema_hash,
      new_schema_hash: data.new_schema_hash,
      severity: data.severity || "WARNING",
      status: "OPEN",
      diff_summary: data.diff_summary,
      ai_analysis: data.ai_analysis,
      ai_patch_snippet: data.ai_patch_snippet,
      detected_at: new Date().toISOString(),
    });
  });

  es.onerror = () => {
    if (state.ssePaused) return;
    setSSEState("error", "SSE Reconnecting&hellip;");
  };
}

function toggleSsePause() {
  state.ssePaused = !state.ssePaused;
  if (state.ssePaused) {
    if (state.eventSource) {
      state.eventSource.close();
      state.eventSource = null;
    }
    setSSEState("connecting", "Live stream paused");
    els.btnSsePause.textContent = "&#9658;";
    els.btnSsePause.title = "Resume live event stream";
  } else {
    els.btnSsePause.textContent = "&#10074;&#10074;";
    els.btnSsePause.title = "Pause live event stream";
    connectSSE();
  }
}

function exportIncidents() {
  const filtered = state.incidents.filter(matchesFilters);
  if (filtered.length === 0) {
    return;
  }

  const timestamp = new Date().toISOString().replace(/[:.]/g, "-");
  const jsonBlob = new Blob([JSON.stringify(filtered, null, 2)], {
    type: "application/json",
  });
  const url = URL.createObjectURL(jsonBlob);
  const a = document.createElement("a");
  a.href = url;
  a.download = `endpoint-lighthouse-incidents-${timestamp}.json`;
  document.body.appendChild(a);
  a.click();
  document.body.removeChild(a);
  URL.revokeObjectURL(url);
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
