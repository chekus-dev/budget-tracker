const form = document.querySelector('form[action="/add"]');
const container = document.getElementById('expenses-container');

if (form && container) {
    const amountInput = form.amount;
    if (amountInput) {
        amountInput.addEventListener('input', () => {
            const totalCard = document.getElementById('total-card');
            const totalAmountEl = document.getElementById('total-amount');
            if (!totalCard || !totalAmountEl) return;
            const baseTotal = parseFloat(totalCard.dataset.total) || 0;
            const typed = parseFloat(amountInput.value);
            totalAmountEl.textContent = '₦' + (isNaN(typed) ? baseTotal : baseTotal + typed).toFixed(2);
        });
    }

    form.addEventListener('submit', function (e) {
        e.preventDefault();
        const description = form.description.value.trim();
        const amount = parseFloat(form.amount.value);
        if (!description) { alert('Please enter a description.'); return; }
        if (isNaN(amount) || amount <= 0) { alert('Amount must be a positive number.'); return; }
        fetch('/add', { method: 'POST', headers: { 'X-Requested-With': 'fetch' }, body: new FormData(form) })
            .then(res => res.text())
            .then(html => {
                container.innerHTML = html;
                form.reset();
                attachDeleteHandlers();
                applyExpenseIcons();
                updateSummary();
            })
            .catch(err => alert('Failed to add expense: ' + err));
    });
}

function attachDeleteHandlers() {
    document.querySelectorAll('.delete-btn').forEach(btn => {
        btn.addEventListener('click', function () {
            if (!confirm('Delete this expense?')) return;
            fetch('/delete/' + btn.dataset.id, { method: 'POST', headers: { 'X-Requested-With': 'fetch' } })
                .then(res => res.text())
                .then(html => {
                    container.innerHTML = html;
                    attachDeleteHandlers();
                    applyExpenseIcons();
                    updateSummary();
                })
                .catch(err => alert('Failed to delete: ' + err));
        });
    });
}

// Keyword -> icon rules for expense descriptions. Matched in order, first hit wins.
const EXPENSE_ICON_RULES = [
    { icon: '🍔', keywords: ['food', 'grocery', 'groceries', 'restaurant', 'lunch', 'dinner', 'breakfast', 'snack', 'coffee'] },
    { icon: '💡', keywords: ['bill', 'bills', 'electric', 'electricity', 'utility', 'utilities', 'water', 'power'] },
    { icon: '🚌', keywords: ['transport', 'bus', 'taxi', 'uber', 'bolt', 'fuel', 'fare', 'train', 'petrol', 'gas'] },
    { icon: '🏥', keywords: ['health', 'hospital', 'doctor', 'pharmacy', 'medicine', 'clinic', 'healthcare'] },
    { icon: '🛍️', keywords: ['shopping', 'clothes', 'clothing', 'mall', 'store'] },
    { icon: '🎬', keywords: ['movie', 'entertainment', 'netflix', 'cinema', 'game', 'games'] },
    { icon: '📚', keywords: ['book', 'books', 'school', 'tuition', 'course', 'education'] },
    { icon: '🏠', keywords: ['rent', 'housing', 'mortgage'] },
    { icon: '📶', keywords: ['internet', 'wifi', 'data', 'airtime', 'subscription', 'phone'] },
];
const EXPENSE_ICON_DEFAULT = '🧾';

function iconForDescription(description) {
    const text = (description || '').toLowerCase();
    for (const rule of EXPENSE_ICON_RULES) {
        if (rule.keywords.some(k => text.includes(k))) return rule.icon;
    }
    return EXPENSE_ICON_DEFAULT;
}

function applyExpenseIcons() {
    document.querySelectorAll('.expense-item').forEach(item => {
        const nameEl = item.querySelector('.expense-name');
        const iconEl = item.querySelector('.expense-icon');
        if (!nameEl || !iconEl) return;
        iconEl.textContent = iconForDescription(nameEl.textContent);
    });
}

function currency(n) {
    return '₦' + n.toFixed(2);
}

function updateSummary() {
    const budgetLimit = window.BUDGET_LIMIT || 0;
    const amountEls = container.querySelectorAll('.expense-amount');
    let total = 0;
    amountEls.forEach(el => {
        const n = parseFloat((el.textContent || '').replace(/[^0-9.]/g, ''));
        if (!isNaN(n)) total += n;
    });
    const count = amountEls.length;

    const balanceEl = document.getElementById('home-balance');
    if (balanceEl) balanceEl.textContent = currency(total);

    const countEl = document.getElementById('home-stat-count');
    if (countEl) countEl.textContent = count;

    if (budgetLimit > 0) {
        const pct = (total / budgetLimit) * 100;
        const pctRounded = Math.round(pct);

        const pctEl = document.getElementById('home-stat-pct');
        if (pctEl) pctEl.textContent = pctRounded + '%';

        const budgetAmountEl = document.getElementById('budget-card-amount');
        if (budgetAmountEl) budgetAmountEl.textContent = currency(total);

        const fillEl = document.getElementById('progress-fill');
        if (fillEl) {
            fillEl.style.width = Math.min(pct, 100).toFixed(1) + '%';
            fillEl.classList.remove('warning', 'danger');
            if (total >= budgetLimit) fillEl.classList.add('danger');
            else if (total >= budgetLimit * 0.8) fillEl.classList.add('warning');
        }

        const textEl = document.getElementById('progress-text');
        if (textEl) textEl.textContent = pctRounded + '% of ' + currency(budgetLimit).replace('.00', '');

        const alertDanger = document.getElementById('alert-danger');
        const alertWarning = document.getElementById('alert-warning');
        if (alertDanger) {
            alertDanger.classList.toggle('show', total >= budgetLimit);
            const t = document.getElementById('alert-danger-total');
            const l = document.getElementById('alert-danger-limit');
            if (t) t.textContent = currency(total);
            if (l) l.textContent = currency(budgetLimit);
        }
        if (alertWarning) {
            alertWarning.classList.toggle('show', total >= budgetLimit * 0.8 && total < budgetLimit);
            const t = document.getElementById('alert-warning-total');
            const l = document.getElementById('alert-warning-limit');
            if (t) t.textContent = currency(total);
            if (l) l.textContent = currency(budgetLimit);
        }

        const form = document.getElementById('add-form');
        const limitMsg = document.getElementById('limit-reached-message');
        const limitReached = total >= budgetLimit;
        if (form) form.style.display = limitReached ? 'none' : '';
        if (limitMsg) limitMsg.style.display = limitReached ? '' : 'none';
    }
}

attachDeleteHandlers();
applyExpenseIcons();