// Admin dashboard: reflect the session-only image upload failure count.
//
// Loaded with `defer` from the admin dashboard. Externalized from an inline
// <script> so the production Content-Security-Policy (script-src 'self', no
// 'unsafe-inline') does not block it.
(() => {
	const uploadFailures = document.querySelector('[data-testid="admin-metric-upload-failures"]');
	if (!uploadFailures) return;
	uploadFailures.textContent = window.sessionStorage.getItem('adminProductImageUploadFailures') || uploadFailures.textContent;
})();
