document.addEventListener('DOMContentLoaded', () => {
    // Listen to the whole document so it works on dynamically loaded modals
    document.body.addEventListener('input', function(e) {
        if (e.target && e.target.name === 'amount') {
            const input = e.target;
            
            // Only format if it's not already a type="number" (which blocks commas)
            if (input.type === 'number') {
                input.type = 'text';
                input.setAttribute('inputmode', 'decimal');
            }

            let cursorPosition = input.selectionStart;
            let originalLength = input.value.length;

            // Remove anything that isn't a digit or a dot
            let val = input.value.replace(/[^\d.]/g, '');
            let parts = val.split('.');
            
            // Format the integer part with commas
            parts[0] = parts[0].replace(/\B(?=(\d{3})+(?!\d))/g, ",");
            
            // Put it back together
            input.value = parts.join('.');

            // Adjust cursor so typing doesn't jump to the end
            let newLength = input.value.length;
            cursorPosition = cursorPosition + (newLength - originalLength);
            input.setSelectionRange(cursorPosition, cursorPosition);
        }
    });

    // Catch the form submission to strip commas before Go parses it
    document.body.addEventListener('submit', function(e) {
        const amountInput = e.target.querySelector('input[name="amount"]');
        if (amountInput) {
            amountInput.value = amountInput.value.replace(/,/g, '');
        }
    });
});
