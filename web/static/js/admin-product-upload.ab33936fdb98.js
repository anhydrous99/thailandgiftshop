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
	const localUploadURL = '/admin/uploads/product-image/local';
	function appendFields(form, fields) {
		Object.entries(fields || {}).forEach(([key, value]) => form.append(key, value));
	}
	async function uploadTarget(target, body) {
		if (target.url === localUploadURL) return;
		const form = new FormData();
		appendFields(form, target.fields);
		form.append('file', body, target.key.split('/').pop());
		const uploaded = await fetch(target.url, { method: 'POST', body: form });
		if (!uploaded.ok) throw new Error('upload failed');
	}
	async function loadImage(file) {
		if ('createImageBitmap' in window) {
			return createImageBitmap(file);
		}
		return new Promise((resolve, reject) => {
			const image = new Image();
			const objectURL = URL.createObjectURL(file);
			image.onload = () => {
				URL.revokeObjectURL(objectURL);
				resolve(image);
			};
			image.onerror = () => {
				URL.revokeObjectURL(objectURL);
				reject(new Error('image decode failed'));
			};
			image.src = objectURL;
		});
	}
	function imageDimensions(image) {
		return { width: image.width || image.naturalWidth, height: image.height || image.naturalHeight };
	}
	function coverCrop(sourceWidth, sourceHeight) {
		const targetRatio = 4 / 3;
		const sourceRatio = sourceWidth / sourceHeight;
		if (sourceRatio > targetRatio) {
			const width = sourceHeight * targetRatio;
			return { x: (sourceWidth - width) / 2, y: 0, width, height: sourceHeight };
		}
		const height = sourceWidth / targetRatio;
		return { x: 0, y: (sourceHeight - height) / 2, width: sourceWidth, height };
	}
	async function renderVariant(image, width, contentType) {
		const height = Math.round(width * 0.75);
		const canvas = document.createElement('canvas');
		canvas.width = width;
		canvas.height = height;
		const context = canvas.getContext('2d');
		if (!context) throw new Error('canvas unavailable');
		const source = imageDimensions(image);
		const crop = coverCrop(source.width, source.height);
		context.drawImage(image, crop.x, crop.y, crop.width, crop.height, 0, 0, width, height);
		return new Promise((resolve, reject) => {
			canvas.toBlob((blob) => {
				if (!blob) {
					reject(new Error('variant encode failed'));
					return;
				}
				resolve(blob);
			}, contentType, contentType === 'image/webp' ? 0.82 : 0.84);
		});
	}
	async function variantPayloads(file, variants) {
		if (!Array.isArray(variants) || variants.length === 0) return [];
		const image = await loadImage(file);
		try {
			const payloads = [];
			for (const variant of variants) {
				const blob = await renderVariant(image, Number(variant.width), variant.content_type);
				payloads.push({ ...variant, blob });
			}
			return payloads;
		} finally {
			if (typeof image.close === 'function') image.close();
		}
	}
	fileInput.addEventListener('change', async () => {
		const file = fileInput.files && fileInput.files[0];
		if (!file) return;
		status.textContent = 'Preparing upload...';
		if (saveButton) saveButton.disabled = true;
		try {
			const headers = { 'Content-Type': 'application/json', 'X-CSRF-Token': csrfInput.value };
			const presign = await fetch('/admin/uploads/product-image/presign', { method: 'POST', headers, body: JSON.stringify({ content_type: file.type, size_bytes: file.size }) }).then((response) => response.json());
			if (!presign.key) throw new Error('presign failed');
			status.textContent = 'Generating responsive images...';
			const variants = await variantPayloads(file, presign.variants);
			status.textContent = 'Uploading image set...';
			await uploadTarget(presign, file);
			for (const variant of variants) {
				await uploadTarget(variant, variant.blob);
			}
			const confirmed = await fetch('/admin/uploads/product-image/confirm', { method: 'POST', headers, body: JSON.stringify({
				key: presign.key,
				content_type: file.type,
				size_bytes: file.size,
				variants: variants.map((variant) => ({ key: variant.key, content_type: variant.content_type, size_bytes: variant.blob.size, width: Number(variant.width) })),
			}) }).then((response) => response.json());
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
