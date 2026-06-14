// Admin product image upload (presign -> S3 POST -> confirm).
//
// Loaded with `defer` from the admin product form. Externalized from an inline
// <script> so the production Content-Security-Policy (script-src 'self', no
// 'unsafe-inline') does not block it. Reads everything it needs from the DOM.
(() => {
	const fileInput = document.querySelector('[data-testid="product-image-input"]');
	const urlInput = document.querySelector('[data-testid="product-image-url"]');
	const tokenInput = document.querySelector('[data-testid="product-image-token"]');
	const status = document.querySelector('[data-testid="product-image-status"]');
	const saveButton = document.querySelector('[data-testid="product-save-button"]');
	const csrfInput = document.querySelector('input[name="csrf_token"]');
	if (!fileInput || !urlInput || !tokenInput || !status || !csrfInput) return;
	fileInput.addEventListener('change', async () => {
		const file = fileInput.files && fileInput.files[0];
		if (!file) return;
		status.textContent = 'Preparing upload...';
		if (saveButton) saveButton.disabled = true;
		try {
			const headers = { 'Content-Type': 'application/json', 'X-CSRF-Token': csrfInput.value };
			const presign = await fetch('/admin/uploads/product-image/presign', { method: 'POST', headers, body: JSON.stringify({ content_type: file.type, size_bytes: file.size }) }).then((response) => response.json());
			if (!presign.key) throw new Error('presign failed');
			if (presign.url !== '/admin/uploads/product-image/local') {
				const form = new FormData();
				Object.entries(presign.fields || {}).forEach(([key, value]) => form.append(key, value));
				form.append('file', file);
				const uploaded = await fetch(presign.url, { method: 'POST', body: form });
				if (!uploaded.ok) throw new Error('upload failed');
			}
			const confirmed = await fetch('/admin/uploads/product-image/confirm', { method: 'POST', headers, body: JSON.stringify({ key: presign.key, content_type: file.type, size_bytes: file.size }) }).then((response) => response.json());
			if (!confirmed.url || !confirmed.token) throw new Error('confirm failed');
			urlInput.value = confirmed.url;
			tokenInput.value = confirmed.token;
			status.textContent = 'Image confirmed: ' + confirmed.url;
			if (saveButton) saveButton.disabled = false;
		} catch (error) {
			urlInput.value = '';
			tokenInput.value = '';
			window.sessionStorage.setItem('adminProductImageUploadFailures', String(Number(window.sessionStorage.getItem('adminProductImageUploadFailures') || '0') + 1));
			status.textContent = 'Image upload failed. Choose a JPEG, PNG, or WebP under 5 MiB.';
		}
	});
})();
