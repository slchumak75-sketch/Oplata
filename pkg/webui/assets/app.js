'use strict';
/*
 * Оплаты — фронтенд GUI (чистый JS, без сборки).
 * Общается с Go-бэкендом через /api/* (см. docs/DATA_MODEL.md).
 * Суммы в API — целые копейки; здесь форматируются как "1 234,56".
 * Роутинг на hash: #/dashboard #/matrix #/suppliers #/supplier/<id> #/operations #/data
 */

const MONTHS_RU = ['', 'январь', 'февраль', 'март', 'апрель', 'май', 'июнь',
  'июль', 'август', 'сентябрь', 'октябрь', 'ноябрь', 'декабрь'];
const STATUS_RU = {paid: 'Оплачено', partial: 'Частично', overdue: 'Просрочено', pending: 'Срок не наступил', due_soon: 'Скоро срок'};

const state = {
  token: '', today: '', settings: {}, suppliers: null, buyers: null,
  dashboardDate: '', matrixAnchor: '', // понедельник отображаемой недели «Таблицы» (ISOString)
  matrixSupplierId: '', // поставщик, выбранный в «Таблице» для панели поставки
  matrixDeliveryDraft: {supplier_id: '', date: '', amount_excl_vat: '', vat: ''},
  opsFilters: {supplier_id: '', kind: 'all', from: '', to: '', q: ''},
  includeArchived: false,
};

function $(sel, root) { return (root || document).querySelector(sel); }
function el(html) { const t = document.createElement('template'); t.innerHTML = html.trim(); return t.content.firstElementChild; }
function esc(s) {
  return String(s == null ? '' : s).replace(/[&<>"']/g, (c) =>
    ({'&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;'}[c]));
}

/* ---------- деньги и даты ---------- */
function fmtMoney(cents) {
  const sign = cents < 0 ? '-' : '';
  const c = Math.abs(Math.round(cents || 0));
  const rub = String(Math.floor(c / 100)).replace(/\B(?=(\d{3})+(?!\d))/g, '\u00A0');
  return sign + rub + ',' + String(c % 100).padStart(2, '0');
}
function fmtMoneyShort(cents) { // без ",00" для компактной матрицы
  if (!cents) return '';
  const s = fmtMoney(cents);
  return s.endsWith(',00') ? s.slice(0, -3) : s;
}
function parseMoneyToCents(raw) {
  if (typeof raw !== 'string') return null;
  let s = raw.replace(/[\s\u00A0]/g, '').replace(',', '.');
  if (s === '' || s === '-' || s === '.') return null;
  if (!/^-?\d*(\.\d{0,2})?$/.test(s)) return null;
  const neg = s.startsWith('-');
  if (neg) s = s.slice(1);
  const parts = s.split('.');
  const rub = parts[0] === '' ? 0 : parseInt(parts[0], 10);
  const kop = parseInt(((parts[1] || '') + '00').slice(0, 2), 10);
  const v = rub * 100 + kop;
  return neg ? -v : v;
}
function fmtDate(iso) {
  if (!iso) return '';
  const p = iso.split('-');
  return p.length === 3 ? p[2] + '.' + p[1] + '.' + p[0] : iso;
}
function addDaysISO(iso, days) {
  const d = new Date(iso + 'T00:00:00Z');
  if (isNaN(d.getTime())) return '';
  d.setUTCDate(d.getUTCDate() + days);
  return d.toISOString().slice(0, 10);
}

/* ---------- API ---------- */
async function api(path, opts) {
  opts = opts || {};
  const headers = Object.assign({}, opts.headers || {});
  if (opts.method && opts.method !== 'GET') headers['X-Oplata-Token'] = state.token;
  let body = opts.body;
  if (opts.json !== undefined) {
    headers['Content-Type'] = 'application/json';
    body = JSON.stringify(opts.json);
  }
  const resp = await fetch(path, {method: opts.method || 'GET', headers, body});
  if (resp.status === 204) return null;
  const text = await resp.text();
  let data = null;
  try { data = JSON.parse(text); } catch (e) { data = null; }
  if (!resp.ok) {
    throw new Error((data && data.error) || text || ('HTTP ' + resp.status));
  }
  return data;
}

/* ---------- тосты и модалки ---------- */
function toast(msg, kind) {
  const node = el('<div class="toast ' + (kind || '') + '">' + esc(msg) + '</div>');
  $('#toasts').appendChild(node);
  setTimeout(() => node.remove(), kind === 'error' ? 7000 : 3500);
}
function openModal(html, wide) {
  closeModal();
  const backdrop = el('<div class="modal-backdrop"><div class="modal' + (wide ? ' wide' : '') + '">' + html + '</div></div>');
  backdrop.addEventListener('click', (e) => { if (e.target === backdrop) closeModal(); });
  $('#modalRoot').appendChild(backdrop);
  return backdrop.querySelector('.modal');
}
function closeModal() { $('#modalRoot').innerHTML = ''; }
function confirmDialog(title, text, okLabel) {
  return new Promise((resolve) => {
    const modal = openModal('<h3>' + esc(title) + '</h3><p>' + esc(text) + '</p>' +
      '<div class="modal-actions"><button class="btn" data-act="cancel">Отмена</button>' +
      '<button class="btn btn-danger" data-act="ok">' + esc(okLabel || 'Удалить') + '</button></div>');
    modal.querySelector('[data-act=cancel]').onclick = () => { closeModal(); resolve(false); };
    modal.querySelector('[data-act=ok]').onclick = () => { closeModal(); resolve(true); };
  });
}
document.addEventListener('keydown', (e) => { if (e.key === 'Escape') closeModal(); });

/* ---------- загрузка ---------- */
async function loadSuppliersCached() {
  const data = await api('/api/suppliers?date=' + state.today + '&include_archived=1');
  state.suppliers = data.suppliers; // [{supplier:{...}, balance_cents, ...}]
  return state.suppliers;
}
function supplierOptions(selectedId, includeEmpty) {
  const list = state.suppliers || [];
  let html = includeEmpty ? '<option value="">— все поставщики —</option>' : '';
  for (const s of list) {
    const sup = s.supplier;
    const sel = sup.id === selectedId ? ' selected' : '';
    const note = sup.archived ? ' (архив)' : '';
    html += '<option value="' + esc(sup.id) + '"' + sel + '>' + esc(sup.name + note) + '</option>';
  }
  return html;
}
function supplierById(id) {
  const found = (state.suppliers || []).find((s) => s.supplier.id === id);
  return found ? found.supplier : null;
}
async function loadBuyersCached() {
  const data = await api('/api/buyers');
  state.buyers = data.buyers;
  return state.buyers;
}
function buyerById(id) {
  return (state.buyers || []).find((b) => b.id === id) || null;
}

async function boot() {
  try {
    const bootData = await api('/api/bootstrap');
    state.token = bootData.token;
    state.today = bootData.today;
    state.settings = bootData.settings || {};
    state.dashboardDate = state.today;
    state.matrixAnchor = mondayOfISO(state.today); // «Таблица» открывается на текущей неделе
    $('#companyName').textContent = state.settings.company_name || '';
    document.title = 'Оплаты — ' + (state.settings.company_name || '');
    $('#footerInfo').textContent = 'Версия ' + bootData.version + ' · Файл данных: ' + bootData.data_file +
      ' · Поставщиков: ' + bootData.counts.suppliers + ', приходов: ' + bootData.counts.obligations + ', оплат: ' + bootData.counts.payments +
      ', покупателей: ' + (bootData.counts.buyers || 0);
    renderWarnings(bootData.warnings);
    await loadSuppliersCached();
    await loadBuyersCached();
    route();
  } catch (e) {
    $('#view').innerHTML = '<div class="panel"><h2>Нет связи с программой</h2><p>' + esc(e.message) +
      '</p><p class="muted">Проверьте, что окно программы запущено, и обновите страницу.</p></div>';
  }
}
function renderWarnings(warnings) {
  const box = $('#warnings');
  if (!warnings || warnings.length === 0) { box.classList.add('hidden'); box.innerHTML = ''; return; }
  box.classList.remove('hidden');
  box.innerHTML = '<h4>⚠ Проверьте данные после импорта</h4><ul>' +
    warnings.map((w) => '<li>' + esc(w) + '</li>').join('') + '</ul>' +
    '<button class="btn btn-small" id="dismissWarnings">Понятно, скрыть</button>';
  $('#dismissWarnings').onclick = async () => {
    try { await api('/api/settings', {method: 'PUT', json: {clear_warnings: true}}); renderWarnings([]); }
    catch (e) { toast(e.message, 'error'); }
  };
}

/* ---------- роутинг ---------- */
window.addEventListener('hashchange', route);
function route() {
  const hash = location.hash || '#/dashboard';
  const parts = hash.replace(/^#\//, '').split('/');
  const name = parts[0] || 'dashboard';
  document.querySelectorAll('#mainTabs a').forEach((a) => {
    a.classList.toggle('active', a.dataset.tab === name || (name === 'supplier' && a.dataset.tab === 'suppliers'));
  });
  const view = $('#view');
  const run = async () => {
    if (name === 'dashboard') await renderDashboard(view);
    else if (name === 'matrix') await renderMatrix(view);
    else if (name === 'suppliers') await renderSuppliers(view);
    else if (name === 'supplier') await renderSupplierCard(view, decodeURIComponent(parts[1] || ''));
    else if (name === 'buyers') await renderBuyers(view);
    else if (name === 'operations') await renderOperations(view);
    else if (name === 'data') await renderData(view);
    else await renderDashboard(view);
  };
  run().catch((e) => { view.innerHTML = '<div class="panel"><h2>Ошибка</h2><p>' + esc(e.message) + '</p></div>'; });
}

/* ---------- Сводка ---------- */
async function renderDashboard(view) {
  const date = state.dashboardDate || state.today;
  view.innerHTML = '<div class="loading">Загрузка сводки…</div>';
  const d = await api('/api/dashboard?date=' + date);
  const cur = esc(state.settings.currency_label || '');
  const card = (label, cents, cls) =>
    '<div class="card ' + (cls || '') + '"><div class="label">' + label + '</div><div class="value">' + fmtMoney(cents) + ' ' + cur + '</div></div>';
  const supplierRows = d.suppliers
    .filter((s) => state.includeArchived || !s.supplier.archived)
    .slice().sort((a, b) => (b.balance_cents - a.balance_cents) || a.supplier.name.localeCompare(b.supplier.name))
    .map((s) => {
      const overdue = s.overdue_cents > 0 ? '<td class="num overdue">' + fmtMoney(s.overdue_cents) + '</td>' : '<td class="num">—</td>';
      return '<tr><td><a href="#/supplier/' + encodeURIComponent(s.supplier.id) + '">' + esc(s.supplier.name) + '</a></td>' +
        '<td class="num">' + fmtMoney(s.balance_cents) + '</td>' + overdue +
        '<td>' + (s.next_due_date ? fmtDate(s.next_due_date) : '—') + '</td><td class="num">' + (s.next_due_cents ? fmtMoney(s.next_due_cents) : '—') + '</td></tr>';
    }).join('');
  const overdueRows = d.overdue_list.map((i) =>
    '<tr><td><a href="#/supplier/' + encodeURIComponent(i.supplier_id) + '">' + esc(i.supplier_name) + '</a></td><td>' + fmtDate(i.due_date) +
    '</td><td class="num">' + fmtMoney(i.amount_cents) + '</td><td class="num overdue">' + fmtMoney(i.open_cents) +
    '</td><td>' + i.days_overdue + ' дн.</td><td>' + esc(i.doc_number || '') + '</td>' +
    '<td><button class="btn btn-small" data-pay-open="' + esc(i.supplier_id) + '" data-amount="' + i.open_cents + '">Оплатить</button></td></tr>').join('');
  const upcomingRows = d.upcoming_list.map((i) =>
    '<tr><td><a href="#/supplier/' + encodeURIComponent(i.supplier_id) + '">' + esc(i.supplier_name) + '</a></td><td>' + fmtDate(i.due_date) +
    '</td><td class="num">' + fmtMoney(i.open_cents) + '</td><td>' + esc(i.doc_number || '') + '</td></tr>').join('');

  view.innerHTML =
    '<div class="toolbar"><strong>Сводка на дату</strong> <input type="date" id="dashDate" value="' + date + '">' +
    '<label><input type="checkbox" id="chkArchived"' + (state.includeArchived ? ' checked' : '') + '> показывать архивных</label>' +
    '<span class="spacer"></span><button class="btn" onclick="window.print()">🖨 Печать</button></div>' +
    '<div class="cards">' +
    card('Всего задолженность', d.total_debt_cents) +
    card('Просрочено', d.overdue_cents, d.overdue_cents > 0 ? 'red' : '') +
    card('Срок сегодня', d.due_today_cents, d.due_today_cents > 0 ? 'amber' : '') +
    card('Срок в ближайшие 7 дней', d.due_week_cents, d.due_week_cents > 0 ? 'amber' : '') +
    card('Приход за месяц', d.accrued_month_cents) +
    card('Оплачено за месяц', d.paid_month_cents, 'green') +
    (d.total_overpaid_cents > 0 ? card('Переплаты поставщикам', d.total_overpaid_cents) : '') +
    '</div>' +
    '<div class="panel"><h3>Задолженность по поставщикам</h3><div class="table-wrap"><table class="data"><thead><tr>' +
    '<th>Поставщик</th><th class="num">Задолженность</th><th class="num">Просрочено</th><th>Ближайший срок</th><th class="num">Сумма срока</th>' +
    '</tr></thead><tbody>' + supplierRows + '</tbody></table></div></div>' +
    '<div class="panel"><h3>🔴 Просроченные оплаты (' + d.overdue_list.length + ')</h3>' +
    (d.overdue_list.length ? '<div class="table-wrap"><table class="data"><thead><tr><th>Поставщик</th><th>Срок</th><th class="num">Сумма</th><th class="num">Осталось</th><th>Просрочка</th><th>Документ</th><th></th></tr></thead><tbody>' + overdueRows + '</tbody></table></div>' : '<p class="muted">Просроченных оплат нет 🎉</p>') + '</div>' +
    '<div class="panel"><h3>🗓 Ближайшие сроки (14 дней)</h3>' +
    (d.upcoming_list.length ? '<div class="table-wrap"><table class="data"><thead><tr><th>Поставщик</th><th>Срок</th><th class="num">К оплате</th><th>Документ</th></tr></thead><tbody>' + upcomingRows + '</tbody></table></div>' : '<p class="muted">Ближайших сроков нет.</p>') + '</div>';

  $('#dashDate').onchange = (e) => { state.dashboardDate = e.target.value || state.today; renderDashboard(view); };
  $('#chkArchived').onchange = (e) => { state.includeArchived = e.target.checked; renderDashboard(view); };
  bindRowButtons(view);
}
function bindRowButtons(root) {
  root.querySelectorAll('[data-add-obl]').forEach((b) => { b.onclick = () => obligationForm({supplierId: b.dataset.addObl}); });
  root.querySelectorAll('[data-add-pay]').forEach((b) => { b.onclick = () => paymentForm({supplierId: b.dataset.addPay}); });
  root.querySelectorAll('[data-pay-open]').forEach((b) => { b.onclick = () => paymentForm({supplierId: b.dataset.payOpen, amountCents: parseInt(b.dataset.amount, 10)}); });
}

/* ---------- Таблица: одна неделя (понедельник–воскресенье) и панель поставки ---------- */
/*
 * По требованию владельца вкладка показывает ровно 7 дней одной недели вокруг
 * state.matrixAnchor (понедельник). Кнопки влево/вправо сразу перескакивают на
 * предыдущую/следующую неделю и перечитывают /api/matrix только за эти 7 дней.
 * Верхняя полоса над датами — месяц и год. Справа — панель поставки выбранного
 * поставщика. Столбцы нерабочих дней РБ сервер помечает is_day_off; GUI использует
 * отметку только в шапке дат, а поля с суммами не закрашивает.
 */
const WEEKDAYS_SHORT = ['', 'пн', 'вт', 'ср', 'чт', 'пт', 'сб', 'вс'];
let deliveryPreviewSeq = 0; // защита от гонок при предпросмотре срока оплаты

function isoToUTC(iso) { const p = iso.split('-'); return Date.UTC(+p[0], +p[1] - 1, +p[2]); }
function utcToISO(ms) { return new Date(ms).toISOString().slice(0, 10); }
function addDaysISO(iso, n) { return utcToISO(isoToUTC(iso) + n * 86400000); }
function mondayOfISO(iso) {
  const wd = (new Date(isoToUTC(iso)).getUTCDay() + 6) % 7; // 0 = понедельник
  return addDaysISO(iso, -wd);
}
function capitalizeMonth(name) { return name ? name.charAt(0).toUpperCase() + name.slice(1) : ''; }
async function fetchWorkingDue(rawDue) {
  if (!rawDue) return null;
  // Календарь РБ уже приходит в /api/matrix (is_day_off): берём первый рабочий день.
  const cal = await api('/api/matrix?from=' + rawDue + '&to=' + addDaysISO(rawDue, 21));
  const rawDay = cal.days[0] || {};
  const work = cal.days.find((d) => !d.is_day_off);
  return {
    rawDue,
    dueDate: work ? work.date : rawDue,
    adjusted: !!work && work.date !== rawDue,
    reason: rawDay.holiday_name || '',
  };
}

function deliveryPanelHtml(supplier) {
  if (!supplier) {
    return '<h3>Поставка</h3>' +
      '<p class="muted">Нажмите наименование поставщика в таблице — здесь появится ввод поставки.</p>' +
      '<p class="muted">Понадобятся дата поставки, сумма без НДС и сумма НДС. Срок оплаты программа ' +
      'посчитает по отсрочке поставщика и перенесёт с выходного на ближайший рабочий день.</p>';
  }
  if (!state.matrixDeliveryDraft || state.matrixDeliveryDraft.supplier_id !== supplier.id) {
    state.matrixDeliveryDraft = {supplier_id: supplier.id, date: state.today, amount_excl_vat: '', vat: ''};
  }
  const d = state.matrixDeliveryDraft;
  const cur = esc(state.settings.currency_label || '');
  return '<div class="delivery-head"><div><h3>Поставка</h3>' +
    '<div class="delivery-supplier">' + esc(supplier.name) + '</div>' +
    '<div class="muted">Отсрочка: ' + (supplier.deferral_days ? supplier.deferral_days + ' дн.' : '0 дн.') +
    (supplier.unp ? ' · УНП ' + esc(supplier.unp) : '') + '</div></div>' +
    '<a class="btn btn-small" href="#/supplier/' + encodeURIComponent(supplier.id) + '">Карточка</a></div>' +
    '<div class="form-error hidden" id="dError"></div>' +
    '<div class="delivery-form">' +
    '<label class="field">Дата поставки * <input type="date" id="dDeliveryDate" value="' + esc(d.date || state.today) + '"></label>' +
    '<label class="field">Сумма без НДС * <input type="text" class="money" id="dAmountExclVat" inputmode="decimal" placeholder="0,00" value="' + esc(d.amount_excl_vat) + '"></label>' +
    '<label class="field">НДС <input type="text" class="money" id="dVat" inputmode="decimal" placeholder="0,00" value="' + esc(d.vat) + '">' +
    '<span class="hint">Сумма НДС в ' + cur + ', не ставка. Пусто = 0.</span></label>' +
    '<label class="field">Сумма с НДС <input type="text" class="money delivery-total" id="dAmountWithVat" readonly tabindex="-1" placeholder="0,00">' +
    '<span class="hint">Вычисляется: сумма без НДС + НДС. Эта сумма попадёт в «Приход».</span></label>' +
    '<div class="delivery-due"><span>Срок оплаты</span><strong id="dDueDate">—</strong><small id="dDueNote"></small></div>' +
    '</div>' +
    '<div class="delivery-actions"><button class="btn" id="dClear" type="button">Очистить</button>' +
    '<button class="btn btn-primary" id="dSave" type="button">Добавить в «Приход»</button></div>' +
    '<p class="muted delivery-note">Срок оплаты = дата поставки + отсрочка. Если дата выпадает на выходной ' +
    'или праздник РБ, программа сдвигает её на ближайший следующий рабочий день. После сохранения таблица ' +
    'сама перейдёт на неделю срока оплаты.</p>';
}
function bindDeliveryPanel(view, supplier) {
  const panel = $('#deliveryPanel', view);
  if (!panel || !supplier) return;
  const dateEl = $('#dDeliveryDate', panel);
  const exclEl = $('#dAmountExclVat', panel);
  const vatEl = $('#dVat', panel);
  const totalEl = $('#dAmountWithVat', panel);
  const dueEl = $('#dDueDate', panel);
  const dueNoteEl = $('#dDueNote', panel);
  const errorEl = $('#dError', panel);
  const saveBtn = $('#dSave', panel);
  const showError = (msg) => {
    errorEl.textContent = msg || '';
    errorEl.classList.toggle('hidden', !msg);
  };
  const readMoney = (input, blankAsZero) => {
    if (!input.value.trim()) return blankAsZero ? 0 : null;
    return parseMoneyToCents(input.value);
  };
  const updateDue = async () => {
    const seq = ++deliveryPreviewSeq;
    const delivery = dateEl.value;
    if (!delivery) {
      dueEl.textContent = '—';
      dueNoteEl.textContent = 'Укажите дату поставки.';
      return;
    }
    const rawDue = addDaysISO(delivery, supplier.deferral_days || 0);
    dueEl.textContent = fmtDate(rawDue);
    dueNoteEl.textContent = 'Проверка рабочего календаря РБ…';
    try {
      const p = await fetchWorkingDue(rawDue);
      if (seq !== deliveryPreviewSeq || !document.body.contains(panel)) return;
      dueEl.textContent = fmtDate(p.dueDate);
      dueNoteEl.textContent = p.adjusted
        ? 'Перенесён с ' + fmtDate(p.rawDue) + (p.reason ? ': ' + p.reason : ' — нерабочий день') + '.'
        : 'Дата поставки + ' + (supplier.deferral_days || 0) + ' дн. отсрочки.';
    } catch (e) {
      if (seq !== deliveryPreviewSeq) return;
      dueNoteEl.textContent = 'Календарь не проверен в предпросмотре; сервер всё равно перенесёт нерабочий день.';
    }
  };
  const update = (refreshDue) => {
    state.matrixDeliveryDraft = {
      supplier_id: supplier.id,
      date: dateEl.value,
      amount_excl_vat: exclEl.value,
      vat: vatEl.value,
    };
    const excl = readMoney(exclEl, false);
    const vat = readMoney(vatEl, true);
    totalEl.value = (excl === null || vat === null) ? '' : fmtMoney(excl + vat);
    if (refreshDue) updateDue();
  };
  dateEl.addEventListener('input', () => update(true));
  dateEl.addEventListener('change', () => update(true));
  [exclEl, vatEl].forEach((input) => {
    input.addEventListener('input', () => update(false));
    input.addEventListener('change', () => update(false));
  });
  $('#dClear', panel).onclick = () => {
    exclEl.value = '';
    vatEl.value = '';
    showError('');
    update();
    exclEl.focus();
  };
  saveBtn.onclick = async () => {
    showError('');
    const delivery = dateEl.value;
    const excl = readMoney(exclEl, false);
    const vat = readMoney(vatEl, true);
    if (!delivery) { showError('Укажите дату поставки.'); dateEl.focus(); return; }
    if (excl === null || excl < 0) { showError('Укажите неотрицательную сумму без НДС, например 1 200,00.'); exclEl.focus(); return; }
    if (vat === null || vat < 0) { showError('Укажите неотрицательную сумму НДС или оставьте поле пустым.'); vatEl.focus(); return; }
    const total = excl + vat;
    if (total <= 0) { showError('Сумма с НДС должна быть больше нуля.'); exclEl.focus(); return; }
    saveBtn.disabled = true;
    try {
      // due_date намеренно не присылаем: сервер сам посчитает поставка + отсрочка
      // и перенесёт нерабочий день по календарю РБ.
      const created = await api('/api/obligations', {method: 'POST', json: {
        supplier_id: supplier.id,
        amount_cents: total,
        amount_excl_vat_cents: excl,
        vat_cents: vat,
        delivery_date: delivery,
      }});
      await loadSuppliersCached();
      state.matrixDeliveryDraft.amount_excl_vat = '';
      state.matrixDeliveryDraft.vat = '';
      toast('Поставка добавлена в «Приход» на ' + fmtDate(created.due_date), 'ok');
      await renderMatrix(view, mondayOfISO(created.due_date));
    } catch (e) {
      showError(e.message);
      saveBtn.disabled = false;
    }
  };
  update(true);
}

async function renderMatrix(view, targetMonday) {
  const anchor = mondayOfISO(targetMonday || state.matrixAnchor || state.today);
  state.matrixAnchor = anchor;
  const from = anchor;
  const to = addDaysISO(anchor, 6); // ровно одна неделя: понедельник–воскресенье
  view.innerHTML = '<div class="loading">Загрузка таблицы…</div>';
  const m = await api('/api/matrix?from=' + from + '&to=' + to);

  // Верхняя полоса над датами — только месяц и год (без «Поставщик/Задолженность»).
  // Первый сегмент дополнительно накрывает две левые колонки, поэтому полоса сплошная.
  let monthHeaders = '';
  for (let i = 0; i < m.days.length;) {
    const d0 = m.days[i];
    let n = 0;
    while (i + n < m.days.length && m.days[i + n].month === d0.month && m.days[i + n].year === d0.year) n++;
    const leftCols = monthHeaders === '' ? 2 : 0;
    monthHeaders += '<th colspan="' + (n * 2 + leftCols) + '" class="month-seg">' +
      capitalizeMonth(MONTHS_RU[d0.month]) + ' ' + d0.year + '</th>';
    i += n;
  }
  const dayCls = (d, first) =>
    (d.is_day_off ? ' dayoff' : '') + (first && d.weekday === 1 ? ' week-start' : '') +
    (d.date === state.today ? ' today' : '');
  // По требованию владельца поля с данными не закрашиваем: для ячеек сумм
  // оставляем только границу начала недели, без dayoff/today фона.
  const dataDayCls = (d, first) => (first && d.weekday === 1 ? ' week-start' : '');
  const dayHeaders = m.days.map((d) => {
    const tip = d.holiday_name ? ' title="' + esc(d.holiday_name) + '"' : '';
    return '<th colspan="2" class="day-head' + dayCls(d, true) + '"' + tip + '>' +
      d.day + ' ' + WEEKDAYS_SHORT[d.weekday] + '</th>';
  }).join('');
  const kindHeaders = m.days.map((d) =>
    '<th class="' + dayCls(d, true) + '">Приход</th><th class="' + dayCls(d, false) + '">Оплата</th>').join('');
  const totalCells = m.days.map((d, i) => {
    const t = m.day_totals[i];
    return '<td class="num' + dataDayCls(d, true) + '">' + fmtMoneyShort(t.sobstv_cents) + '</td>' +
      '<td class="num vyplaty' + dataDayCls(d, false) + '">' + (t.vyplaty_cents ? '−' + fmtMoneyShort(t.vyplaty_cents) : '') + '</td>';
  }).join('');
  const bodyRows = m.rows.filter((r) => state.includeArchived || !r.supplier.archived).map((r) => {
    const byDate = {};
    for (const c of r.cells) byDate[c.date] = c;
    const cells = m.days.map((d) => {
      const c = byDate[d.date] || {sobstv_cents: 0, vyplaty_cents: 0};
      const attr = ' data-cell-sup="' + esc(r.supplier.id) + '" data-cell-date="' + d.date + '"';
      return '<td class="cell num' + dataDayCls(d, true) + '"' + attr + '>' + fmtMoneyShort(c.sobstv_cents) + '</td>' +
        '<td class="cell num vyplaty' + dataDayCls(d, false) + '"' + attr + '>' + (c.vyplaty_cents ? '−' + fmtMoneyShort(c.vyplaty_cents) : '') + '</td>';
    }).join('');
    return '<tr' + (r.supplier.id === state.matrixSupplierId ? ' class="selected-supplier"' : '') + '>' +
      '<td class="sticky-1"><button type="button" class="supplier-link" data-delivery-sup="' + esc(r.supplier.id) + '" title="Ввести поставку поставщика">' + esc(r.supplier.name) + '</button></td>' +
      '<td class="sticky-2 num' + (r.range_balance_cents < 0 ? ' neg' : '') + '" title="Задолженность за всё время: ' + esc(fmtMoney(r.balance_cents)) + '">' + fmtMoneyShort(r.range_balance_cents) + '</td>' + cells + '</tr>';
  }).join('');
  const matrixSuppliers = {};
  m.rows.forEach((r) => { matrixSuppliers[r.supplier.id] = r.supplier; });
  const selectedSupplier = matrixSuppliers[state.matrixSupplierId] || supplierById(state.matrixSupplierId);

  view.innerHTML =
    '<div class="toolbar matrix-toolbar">' +
    '<button class="btn" id="weekBack" title="Предыдущая неделя">← Неделя</button>' +
    '<strong class="week-title">Неделя: ' + fmtDate(from) + ' — ' + fmtDate(to) + '</strong>' +
    '<button class="btn" id="weekFwd" title="Следующая неделя">Неделя →</button>' +
    '<button class="btn" id="gotoToday">Сегодня</button>' +
    '<label class="muted">Дата <input type="date" id="gotoDate" value="' + esc(anchor) + '"></label>' +
    '<button class="btn" id="gotoBtn">Перейти</button>' +
    '<span class="spacer"></span><span class="muted">Задолженность за неделю: <strong>' + fmtMoney(m.grand_range_cents) +
    ' ' + esc(state.settings.currency_label || '') + '</strong> · поставщик — ввод поставки · ячейка — операции дня</span></div>' +
    '<div class="matrix-layout"><div class="matrix-main">' +
    '<div class="matrix-wrap week" id="matrixWrap"><table class="matrix week"><thead>' +
    '<tr class="months">' + monthHeaders + '</tr>' +
    '<tr class="days"><th class="sticky-1">ПОСТАВЩИК</th><th class="sticky-2">ЗАДОЛЖ.</th>' + dayHeaders + '</tr>' +
    '<tr class="kinds"><th class="sticky-1"></th><th class="sticky-2">за неделю</th>' + kindHeaders + '</tr>' +
    '</thead><tbody>' +
    '<tr class="total"><td class="sticky-1">ВСЕГО</td><td class="sticky-2 num">' + fmtMoneyShort(m.grand_range_cents) + '</td>' + totalCells + '</tr>' +
    bodyRows + '</tbody></table></div>' +
    '<p class="muted">Показана одна неделя — с понедельника по воскресенье. Кнопки «← Неделя» и «Неделя →» ' +
    'перескакивают сразу на 7 дней. В шапке дат <span class="legend-dayoff"></span> — выходные и праздничные дни по законодательству РБ ' +
    '(с учётом переносов рабочих дней), наведите курсор на дату — покажется название праздника. Жёлтым в шапке отмечен сегодняшний день; поля с суммами не закрашиваются. ' +
    'Дата вверху — срок оплаты с учётом отсрочки; «Приход» — к оплате, «Оплата» показана с минусом. ' +
    'Если за день было несколько операций — в ячейке их сумма.</p></div>' +
    '<aside class="delivery-panel' + (selectedSupplier ? '' : ' hidden') + '" id="deliveryPanel">' + deliveryPanelHtml(selectedSupplier) + '</aside></div>';

  view.querySelectorAll('[data-cell-sup]').forEach((td) => {
    td.onclick = () => dayOperationsModal(td.dataset.cellSup, td.dataset.cellDate);
  });

  bindDeliveryPanel(view, selectedSupplier);
  view.querySelectorAll('[data-delivery-sup]').forEach((btn) => {
    btn.onclick = () => {
      const supplier = matrixSuppliers[btn.dataset.deliverySup] || supplierById(btn.dataset.deliverySup);
      if (!supplier) return;
      state.matrixSupplierId = supplier.id;
      view.querySelectorAll('table.matrix tr.selected-supplier').forEach((tr) => tr.classList.remove('selected-supplier'));
      const row = btn.closest('tr');
      if (row) row.classList.add('selected-supplier');
      const panel = $('#deliveryPanel', view);
      panel.classList.remove('hidden');
      panel.innerHTML = deliveryPanelHtml(supplier);
      bindDeliveryPanel(view, supplier);
      const first = $('#dAmountExclVat', panel);
      if (first) first.focus();
    };
  });

  // Скачкообразная навигация: каждая кнопка сразу показывает соседнюю неделю.
  $('#weekBack').onclick = () => renderMatrix(view, addDaysISO(anchor, -7));
  $('#weekFwd').onclick = () => renderMatrix(view, addDaysISO(anchor, 7));
  $('#gotoToday').onclick = () => renderMatrix(view, mondayOfISO(state.today));
  $('#gotoBtn').onclick = () => { const v = $('#gotoDate').value; if (v) renderMatrix(view, mondayOfISO(v)); };
}

async function dayOperationsModal(supplierId, date) {
  const sup = supplierById(supplierId);
  const data = await api('/api/operations?supplier_id=' + encodeURIComponent(supplierId) +
    '&from=' + date + '&to=' + date + '&limit=200');
  const rows = data.operations.map((o) => operationRowHtml(o)).join('');
  const modal = openModal('<h3>' + esc(sup ? sup.name : '') + ' — ' + fmtDate(date) + '</h3>' +
    (data.operations.length
      ? '<div class="table-wrap"><table class="data"><tbody>' + rows + '</tbody></table></div>'
      : '<p class="muted">Операций за этот день нет.</p>') +
    '<div class="modal-actions"><button class="btn" id="dayClose">Закрыть</button>' +
    '<button class="btn btn-primary" id="dayAddObl">＋ Приход (срок ' + fmtDate(date) + ')</button>' +
    '<button class="btn btn-success" id="dayAddPay">＋ Оплата</button></div>', true);
  $('#dayClose', modal).onclick = () => { closeModal(); };
  $('#dayAddObl', modal).onclick = () => obligationForm({supplierId, dueDate: date});
  $('#dayAddPay', modal).onclick = () => paymentForm({supplierId, date});
  bindOperationRowActions(modal, () => dayOperationsModal(supplierId, date));
}

/* ---------- Поставщики ---------- */
async function renderSuppliers(view) {
  view.innerHTML = '<div class="loading">Загрузка поставщиков…</div>';
  await loadSuppliersCached();
  const rows = state.suppliers
    .filter((s) => state.includeArchived || !s.supplier.archived)
    .map((s) => '<tr><td><a href="#/supplier/' + encodeURIComponent(s.supplier.id) + '">' + esc(s.supplier.name) + '</a>' +
      (s.supplier.archived ? ' <span class="badge pending">архив</span>' : '') + '</td>' +
      '<td>' + esc(s.supplier.unp || '—') + '</td>' +
      '<td class="num">' + fmtMoney(s.balance_cents) + '</td>' +
      '<td class="num' + (s.overdue_cents > 0 ? ' overdue' : '') + '">' + (s.overdue_cents ? fmtMoney(s.overdue_cents) : '—') + '</td>' +
      '<td class="center">' + (s.supplier.deferral_days ? s.supplier.deferral_days + ' дн.' : '—') + '</td>' +
      '<td>' + esc(s.supplier.note || '') + '</td></tr>').join('');
  view.innerHTML =
    '<div class="toolbar"><button class="btn btn-primary" id="btnNewSupplier">＋ Добавить поставщика</button>' +
    '<button class="btn" id="btnSupplierCardList">🏭 Карточка поставщика</button>' +
    '<label><input type="checkbox" id="chkArchived2"' + (state.includeArchived ? ' checked' : '') + '> показывать архивных</label></div>' +
    '<div class="panel"><h3>Поставщики (' + state.suppliers.length + ')</h3><div class="table-wrap"><table class="data">' +
    '<thead><tr><th>Наименование поставщика</th><th>УНП</th><th class="num">Задолженность</th><th class="num">Просрочено</th><th>Отсрочка</th><th>Примечание</th></tr></thead>' +
    '<tbody>' + rows + '</tbody></table></div></div>';
  $('#btnNewSupplier').onclick = () => supplierForm(null);
  $('#btnSupplierCardList').onclick = () => supplierCardModal();
  $('#chkArchived2').onchange = (e) => { state.includeArchived = e.target.checked; renderSuppliers(view); };
}

/* ---------- Покупатели (справочник) ---------- */
async function renderBuyers(view) {
  view.innerHTML = '<div class="loading">Загрузка покупателей…</div>';
  await loadBuyersCached();
  const query = (state.buyersQuery || '').toLowerCase();
  const rows = (state.buyers || [])
    .filter((b) => !query || (b.name + ' ' + (b.unp || '') + ' ' + (b.note || '')).toLowerCase().includes(query))
    .map((b) => '<tr><td>' + esc(b.name) + '</td><td>' + esc(b.unp || '—') + '</td><td>' + esc(b.note || '') + '</td>' +
      '<td class="nowrap"><button class="btn btn-small" data-edit-buyer="' + esc(b.id) + '">Изм.</button> ' +
      '<button class="btn btn-small btn-danger" data-del-buyer="' + esc(b.id) + '">Удалить покупателя</button></td></tr>').join('');
  view.innerHTML =
    '<div class="toolbar"><button class="btn btn-primary" id="btnNewBuyer">＋ Добавить покупателя</button>' +
    '<button class="btn" id="btnBuyerCardList">🛒 Карточка покупателя</button>' +
    '<input type="text" id="buyerSearch" placeholder="Поиск по наименованию или УНП" value="' + esc(state.buyersQuery || '') + '" style="min-width:260px"></div>' +
    '<div class="panel"><h3>Покупатели (' + (state.buyers || []).length + ')</h3><div class="table-wrap"><table class="data">' +
    '<thead><tr><th>Наименование покупателя</th><th>УНП</th><th>Примечание</th><th></th></tr></thead>' +
    '<tbody>' + (rows || '<tr><td colspan="4" class="muted">Покупателей пока нет. Добавьте первого кнопкой выше.</td></tr>') + '</tbody></table></div>' +
    '<p class="muted">Справочник контрагентов: хранятся наименование и УНП. Операции с покупателями программа пока не ведёт.</p></div>';
  $('#btnNewBuyer').onclick = () => buyerForm(null);
  $('#btnBuyerCardList').onclick = () => buyerCardModal();
  let searchTimer = null;
  $('#buyerSearch').oninput = (e) => {
    clearTimeout(searchTimer);
    searchTimer = setTimeout(() => { state.buyersQuery = e.target.value.trim(); renderBuyers(view); }, 300);
  };
  view.querySelectorAll('[data-edit-buyer]').forEach((b) => { b.onclick = () => buyerForm(buyerById(b.dataset.editBuyer)); });
  view.querySelectorAll('[data-del-buyer]').forEach((b) => {
    b.onclick = async () => {
      const buy = buyerById(b.dataset.delBuyer);
      if (!await confirmDialog('Удаление покупателя', 'Удалить покупателя «' + (buy ? buy.name : '') + '»?')) return;
      try {
        await api('/api/buyers/' + encodeURIComponent(b.dataset.delBuyer), {method: 'DELETE'});
        toast('Покупатель удалён', 'ok');
        await loadBuyersCached();
        renderBuyers(view);
      } catch (e) { toast(e.message, 'error'); }
    };
  });
}

/* ---------- Карточка поставщика ---------- */
function balanceChartSVG(running) {
  if (!running || running.length === 0) return '<p class="muted">Нет операций для графика.</p>';
  const w = 900, h = 170, padL = 8, padR = 8, padT = 14, padB = 22;
  const values = running.map((p) => p.balance_cents);
  const minV = Math.min(0, Math.min.apply(null, values));
  const maxV = Math.max(0, Math.max.apply(null, values));
  const span = (maxV - minV) || 1;
  const x = (i) => padL + (w - padL - padR) * (running.length === 1 ? 0.5 : i / (running.length - 1));
  const y = (v) => padT + (h - padT - padB) * (1 - (v - minV) / span);
  const points = running.map((p, i) => x(i).toFixed(1) + ',' + y(p.balance_cents).toFixed(1)).join(' ');
  const zeroY = y(0).toFixed(1);
  const first = running[0], last = running[running.length - 1];
  return '<svg class="chart" viewBox="0 0 ' + w + ' ' + h + '" preserveAspectRatio="none">' +
    '<line x1="0" y1="' + zeroY + '" x2="' + w + '" y2="' + zeroY + '" stroke="#cbd5e1" stroke-dasharray="4 3"/>' +
    '<polyline fill="none" stroke="#2563eb" stroke-width="2" points="' + points + '"/>' +
    '<text x="10" y="12" font-size="11" fill="#68707d">макс: ' + esc(fmtMoney(maxV)) + '</text>' +
    '<text x="10" y="' + (h - 6) + '" font-size="11" fill="#68707d">' + fmtDate(first.date) + '</text>' +
    '<text x="' + (w - 10) + '" y="' + (h - 6) + '" font-size="11" fill="#68707d" text-anchor="end">' + fmtDate(last.date) + ' · ' + esc(fmtMoney(last.balance_cents)) + '</text></svg>';
}
async function renderSupplierCard(view, supplierId) {
  view.innerHTML = '<div class="loading">Загрузка карточки…</div>';
  const c = await api('/api/suppliers/' + encodeURIComponent(supplierId) + '/card?date=' + state.today);
  const sup = c.supplier;
  const oblRows = c.obligations.map((st) => {
    const o = st.obligation;
    return '<tr><td>' + fmtDate(o.due_date) + '</td><td>' + (o.delivery_date ? fmtDate(o.delivery_date) : '—') + '</td>' +
      '<td class="num">' + fmtMoney(o.amount_cents) + '</td><td class="num">' + fmtMoney(st.paid_cents) + '</td>' +
      '<td class="num' + (st.status === 'overdue' ? ' overdue' : '') + '">' + fmtMoney(st.open_cents) + '</td>' +
      '<td><span class="badge ' + st.status + '">' + STATUS_RU[st.status] + (st.days_overdue ? ' ' + st.days_overdue + ' дн.' : '') + '</span></td>' +
      '<td>' + esc(o.doc_number || '') + '</td><td>' + esc(o.comment || '') +
      ((o.amount_excl_vat_cents || o.vat_cents)
        ? '<div class="muted">без НДС: ' + fmtMoney(o.amount_excl_vat_cents || 0) + '; НДС: ' + fmtMoney(o.vat_cents || 0) + '</div>' : '') + '</td>' +
      '<td class="nowrap"><button class="btn btn-small" data-edit-op="obligation" data-id="' + esc(o.id) + '">Изм.</button> ' +
      '<button class="btn btn-small" data-del-op="obligation" data-id="' + esc(o.id) + '">Удал.</button></td></tr>';
  }).join('');
  const payRows = c.payments.map((p) =>
    '<tr><td>' + fmtDate(p.date) + '</td><td class="num">' + fmtMoney(p.amount_cents) + '</td><td>' + esc(p.doc_number || '') +
    '</td><td>' + esc(p.comment || '') + '</td>' +
    '<td class="nowrap"><button class="btn btn-small" data-edit-op="payment" data-id="' + esc(p.id) + '">Изм.</button> ' +
    '<button class="btn btn-small" data-del-op="payment" data-id="' + esc(p.id) + '">Удал.</button></td></tr>').join('');
  view.innerHTML =
    '<div class="toolbar"><a class="btn" href="#/suppliers">← К поставщикам</a>' +
    '<button class="btn btn-primary" data-add-obl="' + esc(sup.id) + '">＋ Приход</button>' +
    '<button class="btn btn-success" data-add-pay="' + esc(sup.id) + '">＋ Оплата</button>' +
    (c.overdue_cents > 0 ? '<button class="btn btn-danger" id="payOverdue">Оплатить просроченное: ' + fmtMoney(c.overdue_cents) + '</button>' : '') +
    '<span class="spacer"></span><button class="btn" id="editSupplierBtn">Изм. поставщика</button><button class="btn" onclick="window.print()">🖨 Печать</button></div>' +
    '<div class="panel"><h2>' + esc(sup.name) + (sup.archived ? ' <span class="badge pending">архив</span>' : '') + '</h2>' +
    '<div class="kv">' +
    '<div><div class="k">Задолженность</div><div class="v">' + fmtMoney(c.balance_cents) + ' ' + esc(state.settings.currency_label || '') + '</div></div>' +
    '<div><div class="k">Просрочено</div><div class="v" style="color:var(--red)">' + fmtMoney(c.overdue_cents) + '</div></div>' +
    '<div><div class="k">Переплата</div><div class="v">' + fmtMoney(c.overpaid_cents) + '</div></div>' +
    '<div><div class="k">Отсрочка</div><div class="v">' + (sup.deferral_days ? sup.deferral_days + ' дн.' : 'не задана') + '</div></div>' +
    '<div><div class="k">УНП</div><div class="v">' + esc(sup.unp || 'не указан') + '</div></div></div>' +
    (sup.note ? '<p class="muted">' + esc(sup.note) + '</p>' : '') +
    balanceChartSVG(c.running) + '</div>' +
    '<div class="panel"><h3>Приход — погашение по FIFO от раннего срока</h3><div class="table-wrap"><table class="data">' +
    '<thead><tr><th>Срок оплаты</th><th>Поставка</th><th class="num">Сумма</th><th class="num">Оплачено</th><th class="num">Осталось</th><th>Статус</th><th>Документ</th><th>Комментарий</th><th></th></tr></thead><tbody>' +
    (oblRows || '<tr><td colspan="9" class="muted">Приходов нет.</td></tr>') + '</tbody></table></div></div>' +
    '<div class="panel"><h3>Оплата</h3><div class="table-wrap"><table class="data">' +
    '<thead><tr><th>Дата</th><th class="num">Сумма</th><th>Документ</th><th>Комментарий</th><th></th></tr></thead><tbody>' +
    (payRows || '<tr><td colspan="5" class="muted">Оплат нет.</td></tr>') + '</tbody></table></div></div>';
  bindRowButtons(view);
  $('#editSupplierBtn').onclick = () => supplierForm(sup);
  const payOverdue = $('#payOverdue');
  if (payOverdue) payOverdue.onclick = () => paymentForm({supplierId: sup.id, amountCents: c.overdue_cents});
  bindOperationRowActions(view, () => renderSupplierCard(view, supplierId));
}

/* ---------- Операции (журнал) ---------- */
function operationRowHtml(o, showActions = true) {
  const kind = o.kind === 'obligation'
    ? '<span class="badge partial">Приход</span>' : '<span class="badge paid">Оплата</span>';
  const vatDetails = (o.kind === 'obligation' && (o.amount_excl_vat_cents || o.vat_cents))
    ? '<div class="muted">без НДС: ' + fmtMoney(o.amount_excl_vat_cents || 0) + '; НДС: ' + fmtMoney(o.vat_cents || 0) + '</div>'
    : '';
  const actions = showActions
    ? '<td class="nowrap"><button class="btn btn-small" data-edit-op="' + o.kind + '" data-id="' + esc(o.id) + '">Изм.</button> ' +
      '<button class="btn btn-small" data-del-op="' + o.kind + '" data-id="' + esc(o.id) + '">Удал.</button></td>'
    : '';
  return '<tr><td>' + kind + '</td><td>' + esc(o.supplier_name) + '</td><td class="nowrap">' + fmtDate(o.date) + '</td>' +
    '<td class="num">' + fmtMoney(o.amount_cents) + '</td><td>' + esc(o.doc_number || '') + '</td><td>' + esc(o.comment || '') + vatDetails + '</td>' +
    actions + '</tr>';
}
function bindOperationRowActions(root, refresh) {
  root.querySelectorAll('[data-edit-op]').forEach((b) => {
    b.onclick = async () => {
      const kind = b.dataset.editOp, id = b.dataset.id;
      const data = await api('/api/operations?limit=5000');
      const op = data.operations.find((o) => o.id === id && o.kind === kind);
      if (!op) { toast('Операция не найдена', 'error'); return; }
      if (kind === 'obligation') obligationForm({existing: op, refresh});
      else paymentForm({existing: op, refresh});
    };
  });
  root.querySelectorAll('[data-del-op]').forEach((b) => {
    b.onclick = async () => {
      if (!await confirmDialog('Удаление операции', 'Операция будет удалена без возможности восстановления (страховка — резервные копии в папке данных).')) return;
      const path = b.dataset.delOp === 'obligation' ? '/api/obligations/' : '/api/payments/';
      try {
        await api(path + encodeURIComponent(b.dataset.id), {method: 'DELETE'});
        toast('Операция удалена', 'ok');
        await loadSuppliersCached();
        if (refresh) refresh(); else route();
      } catch (e) { toast(e.message, 'error'); }
    };
  });
}
async function renderOperations(view) {
  await loadSuppliersCached();
  const f = state.opsFilters;
  const params = new URLSearchParams();
  if (f.supplier_id) params.set('supplier_id', f.supplier_id);
  if (f.kind) params.set('kind', f.kind);
  if (f.from) params.set('from', f.from);
  if (f.to) params.set('to', f.to);
  if (f.q) params.set('q', f.q);
  view.innerHTML = '<div class="loading">Загрузка операций…</div>';
  const data = await api('/api/operations?' + params.toString());
  const rows = data.operations.map((o) => operationRowHtml(o, false)).join('');
  view.innerHTML =
    '<div class="toolbar">' +
    '<select id="fSupplier">' + supplierOptions(f.supplier_id, true) + '</select>' +
    '<select id="fKind"><option value="all"' + (f.kind === 'all' ? ' selected' : '') + '>Все операции</option>' +
    '<option value="obligation"' + (f.kind === 'obligation' ? ' selected' : '') + '>Только Приход</option>' +
    '<option value="payment"' + (f.kind === 'payment' ? ' selected' : '') + '>Только оплаты</option></select>' +
    '<input type="date" id="fFrom" value="' + esc(f.from) + '" title="С даты">' +
    '<input type="date" id="fTo" value="' + esc(f.to) + '" title="По дату">' +
    '<input type="text" id="fQ" placeholder="Поиск: поставщик, документ, комментарий" value="' + esc(f.q) + '" style="min-width:250px">' +
    '<button class="btn" id="fClear">Сбросить</button>' +
    '<span class="spacer"></span><span class="muted">Найдено: ' + data.total + (data.total > data.operations.length ? ' (показано ' + data.operations.length + ')' : '') + '</span></div>' +
    '<div class="panel"><h3>Журнал операций</h3><div class="table-wrap"><table class="data">' +
    '<thead><tr><th>Тип</th><th>Поставщик</th><th>Дата</th><th class="num">Сумма</th><th>Документ</th><th>Комментарий</th></tr></thead>' +
    '<tbody>' + (rows || '<tr><td colspan="6" class="muted">Ничего не найдено.</td></tr>') + '</tbody></table></div>' +
    '<p class="muted">Дата прихода — срок оплаты (с учётом отсрочки). Новые операции — кнопками «＋ Приход» и «＋ Оплата» в шапке.</p></div>';
  const apply = () => {
    state.opsFilters = {
      supplier_id: $('#fSupplier').value, kind: $('#fKind').value,
      from: $('#fFrom').value, to: $('#fTo').value, q: $('#fQ').value.trim(),
    };
    renderOperations(view);
  };
  ['fSupplier', 'fKind', 'fFrom', 'fTo'].forEach((id) => { $('#' + id).onchange = apply; });
  let timer = null;
  $('#fQ').oninput = () => { clearTimeout(timer); timer = setTimeout(apply, 400); };
  $('#fClear').onclick = () => { state.opsFilters = {supplier_id: '', kind: 'all', from: '', to: '', q: ''}; renderOperations(view); };
}

/* ---------- Экран «Данные»: импорт/экспорт/настройки ---------- */
async function renderData(view) {
  const bootData = await api('/api/bootstrap');
  const st = bootData.settings;
  const exportYear = new Date().getFullYear();
  view.innerHTML =
    '<div class="panel"><h3>📥 Импорт из Excel</h3>' +
    '<p>Формат файла — как «Оплаты Строймаркет.xlsx»: лист называется годом, колонки попарно «Собств»/«Выплаты» на каждый день (в интерфейсе программы — «Приход»/«Оплата»).</p>' +
    '<div class="form-grid">' +
    '<label class="field">Файл .xlsx <input type="file" id="importFile" accept=".xlsx"></label>' +
    '<label class="field">Год, если лист назван не годом (необязательно) <input type="number" id="importYear" placeholder="например, 2025"></label>' +
    '<label class="field full">Режим <span style="font-weight:400"><label><input type="radio" name="impMode" value="replace" checked> Заменить все данные файлом</label> ' +
    '<label><input type="radio" name="impMode" value="merge"> Добавить к существующим (поставщики сопоставляются по названию)</label></span></label></div>' +
    '<div class="modal-actions"><button class="btn btn-primary" id="btnImport">Импортировать</button></div><div id="importReport"></div></div>' +
    '<div class="panel"><h3>📤 Экспорт</h3><div class="toolbar">' +
    '<label>Год таблицы: <input type="number" id="exportYear" value="' + exportYear + '" style="width:90px"></label>' +
    '<a class="btn" id="btnExportXlsx" href="/api/export.xlsx?year=' + exportYear + '">Скачать Excel (.xlsx)</a>' +
    '<a class="btn" href="/api/export.csv">Скачать операции (.csv)</a>' +
    '<span class="muted">Excel-файл: лист года в исходном формате + лист «Операции» без потерь.</span></div></div>' +
    '<div class="panel"><h3>💾 Резервная копия</h3><div class="toolbar"><a class="btn" href="/api/backup.json">Скачать резервную копию (JSON)</a>' +
    '<label class="field">Восстановить из файла <input type="file" id="restoreFile" accept=".json"></label>' +
    '<button class="btn" id="btnRestore">Восстановить</button></div>' +
    '<p class="muted">Программа также автоматически складывает копии в подпапку <b>backups</b> рядом с файлом данных при каждом сохранении (хранятся последние 20).</p></div>' +
    '<div class="panel"><h3>⚙ Настройки</h3><div class="form-grid">' +
    '<label class="field">Название организации <input type="text" id="setCompany" value="' + esc(st.company_name || '') + '"></label>' +
    '<label class="field">Подпись валюты <input type="text" id="setCurrency" value="' + esc(st.currency_label || '') + '"></label>' +
    '<label class="field">Предупреждать о сроке за дней <input type="number" id="setWarn" min="0" max="365" value="' + (st.warn_days || 0) + '"></label></div>' +
    '<div class="modal-actions"><button class="btn btn-primary" id="btnSettings">Сохранить настройки</button></div></div>' +
    '<div class="panel"><h3>ℹ О программе</h3><p>Версия: <b>' + esc(bootData.version) + '</b><br>Файл данных: <code>' + esc(bootData.data_file) + '</code><br>' +
    'Записей: поставщиков ' + bootData.counts.suppliers + ', приходов ' + bootData.counts.obligations + ', оплат ' + bootData.counts.payments + ', покупателей ' + (bootData.counts.buyers || 0) + '.' +
    (st.seeded_from ? '<br>Стартовые данные загружены из: ' + esc(st.seeded_from) + '.' : '') + '</p></div>' +
    '<div class="panel danger-zone"><h3>⚠ Опасная зона</h3>' +
    '<p>Сброс удаляет всех поставщиков и операции (настройки сохраняются). Перед сбросом скачайте резервную копию.</p>' +
    '<div class="toolbar"><input type="text" id="resetConfirm" placeholder="Введите RESET"><button class="btn btn-danger" id="btnReset">Очистить базу</button></div></div>';

  $('#exportYear').onchange = (e) => { $('#btnExportXlsx').href = '/api/export.xlsx?year=' + (parseInt(e.target.value, 10) || exportYear); };
  $('#btnImport').onclick = async () => {
    const file = $('#importFile').files[0];
    if (!file) { toast('Выберите файл .xlsx', 'error'); return; }
    const mode = view.querySelector('input[name=impMode]:checked').value;
    if (mode === 'replace' && !await confirmDialog('Импорт с заменой', 'ВСЕ текущие данные будут заменены содержимым файла. Продолжить?', 'Заменить')) return;
    const fd = new FormData();
    fd.append('file', file);
    let url = '/api/import?mode=' + mode;
    const y = $('#importYear').value;
    if (y) url += '&year=' + encodeURIComponent(y);
    try {
      const rep = await api(url, {method: 'POST', body: fd});
      $('#importReport').innerHTML = '<div class="panel" style="background:var(--green-bg)"><h4>Импорт выполнен (' + esc(rep.sheet_name) + ', ' + rep.year + ')</h4>' +
        '<p>Поставщиков: ' + rep.suppliers + ', приходов: ' + rep.obligations + ', оплат: ' + rep.payments + '.<br>' +
        'Сумма прихода: ' + fmtMoney(rep.total_sobstv_cents) + ', оплачено: ' + fmtMoney(rep.total_paid_cents) +
        ', задолженность: <b>' + fmtMoney(rep.total_debt_cents) + '</b>.</p>' +
        (rep.warnings && rep.warnings.length ? '<ul>' + rep.warnings.map((w) => '<li>' + esc(w) + '</li>').join('') + '</ul>' : '') + '</div>';
      await loadSuppliersCached();
      toast('Импорт завершён', 'ok');
    } catch (e) { toast(e.message, 'error'); }
  };
  $('#btnRestore').onclick = async () => {
    const file = $('#restoreFile').files[0];
    if (!file) { toast('Выберите файл резервной копии .json', 'error'); return; }
    if (!await confirmDialog('Восстановление', 'Текущие данные будут заменены резервной копией. Продолжить?', 'Восстановить')) return;
    const fd = new FormData();
    fd.append('file', file);
    try {
      await api('/api/restore', {method: 'POST', body: fd});
      toast('База восстановлена', 'ok');
      await loadSuppliersCached();
      await loadBuyersCached();
      renderData(view);
    } catch (e) { toast(e.message, 'error'); }
  };
  $('#btnSettings').onclick = async () => {
    try {
      const res = await api('/api/settings', {
        method: 'PUT',
        json: {company_name: $('#setCompany').value, currency_label: $('#setCurrency').value, warn_days: parseInt($('#setWarn').value, 10) || 0},
      });
      state.settings = res.settings;
      $('#companyName').textContent = state.settings.company_name || '';
      toast('Настройки сохранены', 'ok');
    } catch (e) { toast(e.message, 'error'); }
  };
  $('#btnReset').onclick = async () => {
    if ($('#resetConfirm').value !== 'RESET') { toast('Введите RESET для подтверждения', 'error'); return; }
    try {
      await api('/api/reset', {method: 'POST', json: {confirm: 'RESET'}});
      toast('База очищена', 'ok');
      await loadSuppliersCached();
      await loadBuyersCached();
      location.hash = '#/dashboard';
      route();
    } catch (e) { toast(e.message, 'error'); }
  };
}

/* ---------- Формы ---------- */
function formError(modal, msg) {
  let box = modal.querySelector('.form-error');
  if (!box) {
    box = el('<div class="form-error"></div>');
    modal.insertBefore(box, modal.children[1] || null);
  }
  box.textContent = msg;
}
async function afterMutation(refresh) {
  closeModal();
  await loadSuppliersCached();
  if (refresh) refresh(); else route();
}
function supplierForm(existing, refresh) {
  const s = existing || {name: '', unp: '', deferral_days: 0, note: '', archived: false};
  const modal = openModal('<h3>' + (existing ? 'Карточка поставщика: ' + esc(s.name) : 'Новый поставщик') + '</h3><div class="form-grid">' +
    '<label class="field full">Наименование поставщика * <input type="text" id="supName" value="' + esc(s.name) + '"></label>' +
    '<label class="field">УНП <input type="text" id="supUnp" value="' + esc(s.unp || '') + '" placeholder="9 символов"><span class="hint">Учётный номер плательщика, необязательно.</span></label>' +
    '<label class="field">Отсрочка платежа, дней <input type="number" id="supDeferral" min="0" max="3650" value="' + (s.deferral_days || 0) + '">' +
    '<span class="hint">Срок оплаты будет предлагаться как дата поставки + отсрочка. 0 — вводить срок вручную.</span></label>' +
    '<label class="field">Примечание <input type="text" id="supNote" value="' + esc(s.note || '') + '"></label>' +
    (existing ? '<label class="field full"><span><input type="checkbox" id="supArchived"' + (s.archived ? ' checked' : '') + '> В архиве (скрывать из списков, историю сохранить)</span></label>' : '') +
    '</div><div class="modal-actions">' +
    (existing ? '<button class="btn btn-danger" id="supDelete" style="margin-right:auto">Удалить поставщика</button>' : '') +
    '<button class="btn" id="supCancel">Отмена</button><button class="btn btn-primary" id="supSave">' + (existing ? 'Сохранить' : 'Добавить поставщика') + '</button></div>');
  $('#supCancel', modal).onclick = closeModal;
  $('#supSave', modal).onclick = async () => {
    const payload = {
      name: $('#supName', modal).value.trim(),
      unp: $('#supUnp', modal).value.trim(),
      deferral_days: parseInt($('#supDeferral', modal).value, 10) || 0,
      note: $('#supNote', modal).value.trim(),
      archived: existing ? $('#supArchived', modal).checked : false,
    };
    if (!payload.name) { formError(modal, 'Укажите название поставщика.'); return; }
    try {
      if (existing) await api('/api/suppliers/' + encodeURIComponent(existing.id), {method: 'PUT', json: payload});
      else await api('/api/suppliers', {method: 'POST', json: payload});
      toast('Поставщик сохранён', 'ok');
      await afterMutation(refresh);
    } catch (e) { formError(modal, e.message); }
  };
  const delBtn = $('#supDelete', modal);
  if (delBtn) delBtn.onclick = async () => {
    if (!await confirmDialog('Удаление поставщика', 'Удалить поставщика «' + s.name + '»? Возможно только без операций.')) return;
    try {
      await api('/api/suppliers/' + encodeURIComponent(existing.id), {method: 'DELETE'});
      toast('Поставщик удалён', 'ok');
      location.hash = '#/suppliers';
      await afterMutation(null);
    } catch (e) { toast(e.message, 'error'); }
  };
  const nameInput = $('#supName', modal);
  nameInput.focus();
}

function buyerForm(existing, refresh) {
  const b = existing || {name: '', unp: '', note: ''};
  const modal = openModal('<h3>' + (existing ? 'Карточка покупателя: ' + esc(b.name) : 'Новый покупатель') + '</h3><div class="form-grid">' +
    '<label class="field full">Наименование покупателя * <input type="text" id="buyName" value="' + esc(b.name) + '"></label>' +
    '<label class="field">УНП <input type="text" id="buyUnp" value="' + esc(b.unp || '') + '" placeholder="9 символов"><span class="hint">Учётный номер плательщика, необязательно.</span></label>' +
    '<label class="field">Примечание <input type="text" id="buyNote" value="' + esc(b.note || '') + '"></label>' +
    '</div><div class="modal-actions">' +
    (existing ? '<button class="btn btn-danger" id="buyDelete" style="margin-right:auto">Удалить покупателя</button>' : '') +
    '<button class="btn" id="buyCancel">Отмена</button><button class="btn btn-primary" id="buySave">' + (existing ? 'Сохранить' : 'Добавить покупателя') + '</button></div>');
  $('#buyCancel', modal).onclick = closeModal;
  $('#buySave', modal).onclick = async () => {
    const payload = {
      name: $('#buyName', modal).value.trim(),
      unp: $('#buyUnp', modal).value.trim(),
      note: $('#buyNote', modal).value.trim(),
    };
    if (!payload.name) { formError(modal, 'Укажите наименование покупателя.'); return; }
    try {
      if (existing) await api('/api/buyers/' + encodeURIComponent(existing.id), {method: 'PUT', json: payload});
      else await api('/api/buyers', {method: 'POST', json: payload});
      toast('Покупатель сохранён', 'ok');
      closeModal();
      await loadBuyersCached();
      if (refresh) refresh(); else route();
    } catch (e) { formError(modal, e.message); }
  };
  const delBtn = $('#buyDelete', modal);
  if (delBtn) delBtn.onclick = async () => {
    if (!await confirmDialog('Удаление покупателя', 'Удалить покупателя «' + b.name + '»?')) return;
    try {
      await api('/api/buyers/' + encodeURIComponent(existing.id), {method: 'DELETE'});
      toast('Покупатель удалён', 'ok');
      closeModal();
      await loadBuyersCached();
      if (refresh) refresh(); else route();
    } catch (e) { toast(e.message, 'error'); }
  };
  $('#buyName', modal).focus();
}

// supplierCardModal — окно «Карточка поставщика» из синей шапки:
// выбор поставщика, наименование/УНП/отсрочка, кнопки Добавить/Удалить/Сохранить.
async function supplierCardModal() {
  if (!state.suppliers) await loadSuppliersCached();
  const list = state.suppliers || [];
  const first = list.length ? list[0].supplier : null;
  const modal = openModal('<h3>🏭 Карточка поставщика</h3><div class="form-grid">' +
    '<label class="field full">Поставщик <select id="scSelect">' + supplierOptions(first ? first.id : '', false) + '</select></label>' +
    '<label class="field full">Наименование поставщика * <input type="text" id="scName"></label>' +
    '<label class="field">УНП <input type="text" id="scUnp" placeholder="9 символов"></label>' +
    '<label class="field">Отсрочка платежа, дней <input type="number" id="scDeferral" min="0" max="3650">' +
    '<span class="hint">Срок оплаты = дата поставки + отсрочка. 0 — срок вручную.</span></label>' +
    '<label class="field full">Примечание <input type="text" id="scNote"></label>' +
    '<label class="field full"><span><input type="checkbox" id="scArchived"> В архиве (скрывать из списков, историю сохранить)</span></label>' +
    '</div><div class="modal-actions">' +
    '<button class="btn btn-danger" id="scDelete" style="margin-right:auto">Удалить поставщика</button>' +
    '<button class="btn" id="scClose">Закрыть</button>' +
    '<button class="btn" id="scOpen">Операции и долги →</button>' +
    '<button class="btn btn-success" id="scAdd">＋ Добавить поставщика</button>' +
    '<button class="btn btn-primary" id="scSave">Сохранить</button></div>');
  const fill = () => {
    const sup = supplierById($('#scSelect', modal).value);
    const has = !!sup;
    $('#scName', modal).value = has ? sup.name : '';
    $('#scUnp', modal).value = has ? (sup.unp || '') : '';
    $('#scDeferral', modal).value = has ? (sup.deferral_days || 0) : 0;
    $('#scNote', modal).value = has ? (sup.note || '') : '';
    $('#scArchived', modal).checked = has ? !!sup.archived : false;
    $('#scDelete', modal).disabled = !has;
    $('#scSave', modal).disabled = !has;
    $('#scOpen', modal).disabled = !has;
  };
  $('#scSelect', modal).onchange = fill;
  fill();
  $('#scClose', modal).onclick = closeModal;
  $('#scAdd', modal).onclick = () => supplierForm(null);
  $('#scOpen', modal).onclick = () => {
    const id = $('#scSelect', modal).value;
    closeModal();
    location.hash = '#/supplier/' + encodeURIComponent(id);
    route();
  };
  $('#scSave', modal).onclick = async () => {
    const id = $('#scSelect', modal).value;
    const payload = {
      name: $('#scName', modal).value.trim(),
      unp: $('#scUnp', modal).value.trim(),
      deferral_days: parseInt($('#scDeferral', modal).value, 10) || 0,
      note: $('#scNote', modal).value.trim(),
      archived: $('#scArchived', modal).checked,
    };
    if (!payload.name) { formError(modal, 'Укажите наименование поставщика.'); return; }
    try {
      await api('/api/suppliers/' + encodeURIComponent(id), {method: 'PUT', json: payload});
      toast('Карточка поставщика сохранена', 'ok');
      closeModal();
      await loadSuppliersCached();
      route();
    } catch (e) { formError(modal, e.message); }
  };
  $('#scDelete', modal).onclick = async () => {
    const sup = supplierById($('#scSelect', modal).value);
    if (!sup) return;
    if (!await confirmDialog('Удаление поставщика', 'Удалить поставщика «' + sup.name + '»? Возможно только без операций, иначе используйте архив.')) return;
    try {
      await api('/api/suppliers/' + encodeURIComponent(sup.id), {method: 'DELETE'});
      toast('Поставщик удалён', 'ok');
      closeModal();
      await loadSuppliersCached();
      route();
    } catch (e) { toast(e.message, 'error'); }
  };
}

// buyerCardModal — окно «Карточка покупателя» из синей шапки:
// выбор покупателя, наименование/УНП, кнопки Добавить/Удалить/Сохранить.
async function buyerCardModal() {
  if (!state.buyers) await loadBuyersCached();
  const list = state.buyers || [];
  const options = list.map((b) => '<option value="' + esc(b.id) + '">' + esc(b.name) + '</option>').join('');
  const modal = openModal('<h3>🛒 Карточка покупателя</h3><div class="form-grid">' +
    '<label class="field full">Покупатель <select id="bcSelect">' + options + '</select>' +
    (list.length ? '' : '<span class="hint">Покупателей пока нет — добавьте первого кнопкой ниже.</span>') + '</label>' +
    '<label class="field full">Наименование покупателя * <input type="text" id="bcName"></label>' +
    '<label class="field">УНП <input type="text" id="bcUnp" placeholder="9 символов"></label>' +
    '<label class="field">Примечание <input type="text" id="bcNote"></label>' +
    '</div><div class="modal-actions">' +
    '<button class="btn btn-danger" id="bcDelete" style="margin-right:auto">Удалить покупателя</button>' +
    '<button class="btn" id="bcClose">Закрыть</button>' +
    '<button class="btn btn-success" id="bcAdd">＋ Добавить покупателя</button>' +
    '<button class="btn btn-primary" id="bcSave">Сохранить</button></div>');
  const fill = () => {
    const buy = buyerById($('#bcSelect', modal).value);
    const has = !!buy;
    $('#bcName', modal).value = has ? buy.name : '';
    $('#bcUnp', modal).value = has ? (buy.unp || '') : '';
    $('#bcNote', modal).value = has ? (buy.note || '') : '';
    $('#bcDelete', modal).disabled = !has;
    $('#bcSave', modal).disabled = !has;
  };
  $('#bcSelect', modal).onchange = fill;
  fill();
  $('#bcClose', modal).onclick = closeModal;
  $('#bcAdd', modal).onclick = () => buyerForm(null);
  $('#bcSave', modal).onclick = async () => {
    const id = $('#bcSelect', modal).value;
    const payload = {
      name: $('#bcName', modal).value.trim(),
      unp: $('#bcUnp', modal).value.trim(),
      note: $('#bcNote', modal).value.trim(),
    };
    if (!payload.name) { formError(modal, 'Укажите наименование покупателя.'); return; }
    try {
      await api('/api/buyers/' + encodeURIComponent(id), {method: 'PUT', json: payload});
      toast('Карточка покупателя сохранена', 'ok');
      closeModal();
      await loadBuyersCached();
      route();
    } catch (e) { formError(modal, e.message); }
  };
  $('#bcDelete', modal).onclick = async () => {
    const buy = buyerById($('#bcSelect', modal).value);
    if (!buy) return;
    if (!await confirmDialog('Удаление покупателя', 'Удалить покупателя «' + buy.name + '»?')) return;
    try {
      await api('/api/buyers/' + encodeURIComponent(buy.id), {method: 'DELETE'});
      toast('Покупатель удалён', 'ok');
      closeModal();
      await loadBuyersCached();
      route();
    } catch (e) { toast(e.message, 'error'); }
  };
}

function obligationForm(opts) {
  opts = opts || {};
  const ex = opts.existing; // operationView при редактировании
  const init = {
    supplierId: ex ? ex.supplier_id : (opts.supplierId || (state.suppliers && state.suppliers[0] ? state.suppliers[0].supplier.id : '')),
    amount: ex ? fmtMoney(ex.amount_cents).replace(/\u00A0/g, ' ') : '',
    due: ex ? ex.date : (opts.dueDate || ''),
    delivery: ex ? (ex.delivery_date || '') : '',
    doc: ex ? (ex.doc_number || '') : '',
    comment: ex ? (ex.comment || '') : '',
  };
  const sup0 = supplierById(init.supplierId);
  const modal = openModal('<h3>' + (ex ? 'Редактирование: приход' : 'Новый приход') + '</h3><div class="form-grid">' +
    '<label class="field full">Поставщик * <select id="oSupplier">' + supplierOptions(init.supplierId, false) + '</select></label>' +
    '<label class="field">Сумма к оплате * <input type="text" class="money" id="oAmount" inputmode="decimal" placeholder="0,00" value="' + esc(init.amount) + '"></label>' +
    '<label class="field">№ накладной / счёта <input type="text" id="oDoc" value="' + esc(init.doc) + '"></label>' +
    '<label class="field">Дата поставки <input type="date" id="oDelivery" value="' + esc(init.delivery) + '"><span class="hint">Необязательно.</span></label>' +
    '<label class="field">Отсрочка, дней <input type="number" id="oDeferral" min="0" max="3650" value="' + (sup0 ? sup0.deferral_days || 0 : 0) + '"></label>' +
    '<label class="field">Срок оплаты * <input type="date" id="oDue" value="' + esc(init.due) + '"><span class="hint">Считается: поставка + отсрочка; выходной/праздник РБ автоматически сдвигается на следующий рабочий день. Можно исправить вручную.</span></label>' +
    '<label class="field">Комментарий <input type="text" id="oComment" value="' + esc(init.comment) + '"></label>' +
    '</div><div class="modal-actions"><button class="btn" id="oCancel">Отмена</button><button class="btn btn-primary" id="oSave">Сохранить</button></div>');
  const recalcDue = async () => {
    const delivery = $('#oDelivery', modal).value;
    const deferral = parseInt($('#oDeferral', modal).value, 10) || 0;
    if (!delivery) return;
    const rawDue = addDaysISO(delivery, deferral);
    $('#oDue', modal).value = rawDue;
    try {
      const p = await fetchWorkingDue(rawDue);
      if ($('#oDelivery', modal).value === delivery &&
          (parseInt($('#oDeferral', modal).value, 10) || 0) === deferral) {
        $('#oDue', modal).value = p.dueDate;
      }
    } catch (e) { /* сервер всё равно применит перенос при пустом due_date; здесь остаётся сырая дата */ }
  };
  $('#oSupplier', modal).onchange = (e) => {
    const sup = supplierById(e.target.value);
    if (sup) $('#oDeferral', modal).value = sup.deferral_days || 0;
    recalcDue();
  };
  $('#oDelivery', modal).onchange = recalcDue;
  $('#oDeferral', modal).onchange = recalcDue;
  $('#oCancel', modal).onclick = closeModal;
  $('#oSave', modal).onclick = async () => {
    const cents = parseMoneyToCents($('#oAmount', modal).value);
    if (cents === null || cents <= 0) { formError(modal, 'Укажите положительную сумму, например 1 013,49.'); return; }
    const payload = {
      supplier_id: $('#oSupplier', modal).value, amount_cents: cents,
      due_date: $('#oDue', modal).value, delivery_date: $('#oDelivery', modal).value,
      deferral_days: parseInt($('#oDeferral', modal).value, 10) || 0,
      doc_number: $('#oDoc', modal).value.trim(), comment: $('#oComment', modal).value.trim(),
    };
    // Если запись была создана панелью поставки и итог не меняли — сохранить разбивку НДС.
    if (ex && (ex.amount_excl_vat_cents || ex.vat_cents) &&
        cents === (ex.amount_excl_vat_cents || 0) + (ex.vat_cents || 0)) {
      payload.amount_excl_vat_cents = ex.amount_excl_vat_cents || 0;
      payload.vat_cents = ex.vat_cents || 0;
    }
    if (!payload.supplier_id) { formError(modal, 'Сначала создайте поставщика.'); return; }
    if (!payload.due_date && !payload.delivery_date) { formError(modal, 'Укажите срок оплаты или дату поставки.'); return; }
    try {
      if (ex) await api('/api/obligations/' + encodeURIComponent(ex.id), {method: 'PUT', json: payload});
      else await api('/api/obligations', {method: 'POST', json: payload});
      toast('Приход сохранён', 'ok');
      await afterMutation(opts.refresh);
    } catch (e) { formError(modal, e.message); }
  };
  $('#oAmount', modal).focus();
}

function paymentForm(opts) {
  opts = opts || {};
  const ex = opts.existing;
  const init = {
    supplierId: ex ? ex.supplier_id : (opts.supplierId || (state.suppliers && state.suppliers[0] ? state.suppliers[0].supplier.id : '')),
    amount: ex ? fmtMoney(ex.amount_cents).replace(/\u00A0/g, ' ')
      : (opts.amountCents ? fmtMoney(opts.amountCents).replace(/\u00A0/g, ' ') : ''),
    date: ex ? ex.date : (opts.date || state.today),
    doc: ex ? (ex.doc_number || '') : '',
    comment: ex ? (ex.comment || '') : '',
  };
  const modal = openModal('<h3>' + (ex ? 'Редактирование: оплата' : 'Новая оплата') + '</h3><div class="form-grid">' +
    '<label class="field full">Поставщик * <select id="pSupplier">' + supplierOptions(init.supplierId, false) + '</select>' +
    '<span class="hint" id="pDebtHint"></span></label>' +
    '<label class="field">Сумма оплаты * <input type="text" class="money" id="pAmount" inputmode="decimal" placeholder="0,00" value="' + esc(init.amount) + '">' +
    '<span class="hint">Положительная сумма уменьшает задолженность. Отрицательная — сторно.</span></label>' +
    '<label class="field">Дата оплаты * <input type="date" id="pDate" value="' + esc(init.date) + '"></label>' +
    '<label class="field">№ платёжного документа <input type="text" id="pDoc" value="' + esc(init.doc) + '"></label>' +
    '<label class="field">Комментарий <input type="text" id="pComment" value="' + esc(init.comment) + '"></label>' +
    '</div><div class="modal-actions"><button class="btn" id="pCancel">Отмена</button><button class="btn btn-success" id="pSave">Сохранить</button></div>');
  const debtHint = () => {
    const s = (state.suppliers || []).find((x) => x.supplier.id === $('#pSupplier', modal).value);
    $('#pDebtHint', modal).textContent = s
      ? 'Задолженность сейчас: ' + fmtMoney(s.balance_cents) + ' ' + (state.settings.currency_label || '') +
        (s.overdue_cents ? ', просрочено: ' + fmtMoney(s.overdue_cents) : '')
      : '';
  };
  $('#pSupplier', modal).onchange = debtHint;
  debtHint();
  $('#pCancel', modal).onclick = closeModal;
  $('#pSave', modal).onclick = async () => {
    const cents = parseMoneyToCents($('#pAmount', modal).value);
    if (cents === null || cents === 0) { formError(modal, 'Укажите сумму оплаты, например 1 013,49.'); return; }
    const payload = {
      supplier_id: $('#pSupplier', modal).value, amount_cents: cents,
      date: $('#pDate', modal).value,
      doc_number: $('#pDoc', modal).value.trim(), comment: $('#pComment', modal).value.trim(),
    };
    if (!payload.supplier_id) { formError(modal, 'Сначала создайте поставщика.'); return; }
    if (!payload.date) { formError(modal, 'Укажите дату оплаты.'); return; }
    try {
      if (ex) await api('/api/payments/' + encodeURIComponent(ex.id), {method: 'PUT', json: payload});
      else await api('/api/payments', {method: 'POST', json: payload});
      toast('Оплата сохранена', 'ok');
      await afterMutation(opts.refresh);
    } catch (e) { formError(modal, e.message); }
  };
  $('#pAmount', modal).focus();
}

/* ---------- шапка ---------- */
$('#btnSupplierCard').onclick = () => supplierCardModal();
$('#btnBuyerCard').onclick = () => buyerCardModal();
$('#btnAddObligation').onclick = async () => {
  if (!state.suppliers) await loadSuppliersCached();
  if (!state.suppliers || state.suppliers.length === 0) { toast('Сначала создайте поставщика (вкладка «Поставщики»)', 'error'); return; }
  obligationForm({});
};
$('#btnAddPayment').onclick = async () => {
  if (!state.suppliers) await loadSuppliersCached();
  if (!state.suppliers || state.suppliers.length === 0) { toast('Сначала создайте поставщика (вкладка «Поставщики»)', 'error'); return; }
  paymentForm({});
};

let booted = false;
function bootOnce() { if (!booted) { booted = true; boot(); } }
document.addEventListener('DOMContentLoaded', bootOnce);
if (document.readyState !== 'loading') bootOnce();
