document.addEventListener('DOMContentLoaded', () => {
    const categoryList = document.getElementById('category-list');
    const addCategoryBtn = document.getElementById('add-category-btn');

    categoryList.querySelectorAll('button').forEach(btn => {
        btn.addEventListener('click', () => removeRow(btn));
    });

    addCategoryBtn.addEventListener('click', () => {
        const row = createCategoryRow('');
        categoryList.appendChild(row);
        row.querySelector('input').focus();
    });

    function createCategoryRow(value) {
        const div = document.createElement('div');
        div.className = 'flex items-center gap-2';
        div.innerHTML = `
            <input type="text" name="category" value="${escapeHtml(value)}" placeholder="New category"
                class="flex-1 border border-emerald-100 rounded-lg px-3 py-2 text-emerald-800 focus:outline-none focus:ring-2 focus:ring-emerald-800">
            <button type="button" class="text-emerald-700/40 hover:text-red-600 transition text-sm px-2">✕</button>
        `;
        div.querySelector('button').addEventListener('click', function () { removeRow(this); });
        return div;
    }

    function removeRow(btn) {
        if (categoryList.children.length <= 1) { showToast('You need at least one category.', 'error'); return; }
        btn.closest('div').remove();
    }

    document.querySelector('form').addEventListener('submit', (e) => {
        e.preventDefault();
        const budgetInput = document.querySelector('input[type="number"]');
        const budgetValue = parseFloat(budgetInput.value);
        const currency = document.querySelector('select').value;
        const categories = [...categoryList.querySelectorAll('input[name="category"]')].map(i => i.value.trim()).filter(Boolean);

        if (budgetInput.value && (isNaN(budgetValue) || budgetValue <= 0)) {
            showToast('Budget limit must be a positive number.', 'error'); budgetInput.focus(); return;
        }
        if (categories.length === 0) { showToast('Add at least one category.', 'error'); return; }

        const formData = new FormData();
        formData.append('budget', budgetInput.value || '0');
        formData.append('currency', currency);
        categories.forEach(cat => formData.append('category', cat));

        fetch('/settings/saved', { method: 'POST', body: formData })
            .then(res => { window.location.href = res.redirected ? res.url : '/settings/saved'; })
            .catch(() => showToast('Network error. Try again.', 'error'));
    });

    function showToast(message, type = 'success') {
        document.querySelector('.toast-msg')?.remove();
        const toast = document.createElement('div');
        toast.className = `toast-msg fixed bottom-6 left-1/2 -translate-x-1/2 px-5 py-3 rounded-xl text-sm font-medium shadow-lg z-50 transition-opacity duration-300 ${
            type === 'error' ? 'bg-red-100 text-red-800 border border-red-200' : 'bg-emerald-100 text-emerald-900 border border-emerald-200'
        }`;
        toast.textContent = message;
        document.body.appendChild(toast);
        setTimeout(() => { toast.style.opacity = '0'; setTimeout(() => toast.remove(), 300); }, 3000);
    }

    function escapeHtml(str) {
        return str.replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;').replace(/"/g, '&quot;');
    }
});
