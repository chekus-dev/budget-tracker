document.addEventListener('DOMContentLoaded', () => {
    const isMoneyInput = (el) => {
        if (!el || el.tagName !== 'INPUT') return false;
        if (el.type === 'number') return true;
        const n = (el.name || '').toLowerCase();
        const id = (el.id || '').toLowerCase();
        return n.includes('amount') || n.includes('budget') || n.includes('limit') || 
               id.includes('amount') || id.includes('budget') || id.includes('limit');
    };

    const formatValue = (raw) => {
        let val = raw.replace(/[^\d.]/g, '');
        let parts = val.split('.');
        if (parts.length > 2) parts = [parts[0], parts[1]];
        if (parts[1] && parts[1].length > 2) parts[1] = parts[1].substring(0, 2);
        parts[0] = parts[0].replace(/\B(?=(\d{3})+(?!\d))/g, ",");
        return parts.join('.');
    };

    const processInput = (input) => {
        if (!isMoneyInput(input)) return;
        if (input.type === 'number') {
            input.type = 'text';
            input.setAttribute('inputmode', 'decimal');
        }
        if (input.value && !input.value.includes(',')) {
            input.value = formatValue(input.value);
        }
    };

    document.querySelectorAll('input').forEach(processInput);

    const observer = new MutationObserver((mutations) => {
        mutations.forEach((mutation) => {
            mutation.addedNodes.forEach((node) => {
                if (node.nodeType === 1) {
                    if (node.tagName === 'INPUT') processInput(node);
                    else node.querySelectorAll('input').forEach(processInput);
                }
            });
        });
    });
    observer.observe(document.body, { childList: true, subtree: true });

    document.body.addEventListener('input', function(e) {
        if (!isMoneyInput(e.target)) return;
        const input = e.target;
        if (input.type === 'number') {
            input.type = 'text';
            input.setAttribute('inputmode', 'decimal');
        }
        const rawValue = input.value;
        const cursorPos = input.selectionStart;
        const digitsBefore = rawValue.slice(0, cursorPos).replace(/[^\d]/g, '').length;
        
        input.value = formatValue(rawValue);

        if (digitsBefore > 0) {
            let digitsSeen = 0;
            for (let i = 0; i < input.value.length; i++) {
                if (/\d/.test(input.value[i])) digitsSeen++;
                if (digitsSeen === digitsBefore) {
                    input.setSelectionRange(i + 1, i + 1);
                    break;
                }
            }
        }
    });

    document.body.addEventListener('submit', function(e) {
        e.target.querySelectorAll('input').forEach(input => {
            if (isMoneyInput(input)) input.value = input.value.replace(/,/g, '');
        });
    });
});
