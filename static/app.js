const form = document.querySelector('form');
const container = document.getElementById('expenses-container');
const amountInput = form.amount;

// --- Live total: shows current total + whatever is typed, before submitting ---
function updateLiveTotal() {
    const totalCard = document.getElementById('total-card');
    const totalAmountEl = document.getElementById('total-amount');
    if (!totalCard || !totalAmountEl) return;

    const baseTotal = parseFloat(totalCard.dataset.total) || 0;
    const typedAmount = parseFloat(amountInput.value);

    const preview = isNaN(typedAmount) ? baseTotal : baseTotal + typedAmount;
    totalAmountEl.textContent = '₦' + preview.toFixed(2);
}

amountInput.addEventListener('input', updateLiveTotal);

// --- Add expense (validated, no page reload) ---
form.addEventListener('submit', function (e) {
    e.preventDefault();

    const description = form.description.value.trim();
    const amount = parseFloat(form.amount.value);

    if (description === '') {
        alert('Please enter a description.');
        return;
    }
    if (isNaN(amount) || amount <= 0) {
        alert('Amount must be a positive number.');
        return;
    }

    const formData = new FormData(form);

    fetch('/add', {
        method: 'POST',
        headers: { 'X-Requested-With': 'fetch' },
        body: formData,
    })
        .then((res) => res.text())
        .then((html) => {
            container.innerHTML = html;
            form.reset();
            attachDeleteHandlers();
        })
        .catch((err) => alert('Failed to add expense: ' + err));
});

// --- Delete expense (no page reload) ---
function attachDeleteHandlers() {
    document.querySelectorAll('.delete-btn').forEach((btn) => {
        btn.addEventListener('click', function () {
            const id = btn.dataset.id;
            if (!confirm('Delete this expense?')) return;

            fetch('/delete/' + id, {
                method: 'POST',
                headers: { 'X-Requested-With': 'fetch' },
            })
                .then((res) => res.text())
                .then((html) => {
                    container.innerHTML = html;
                    attachDeleteHandlers();
                })
                .catch((err) => alert('Failed to delete expense: ' + err));
        });
    });
}

// Attach delete handlers on initial page load too
attachDeleteHandlers();