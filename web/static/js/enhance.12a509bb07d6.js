// Progressive enhancement for Thailand Gift Shop.
//
// Loaded with `defer`. The site is fully functional without it: every form is a
// real POST and the server validates everything. These behaviors are additive
// only and must never become required for a flow to work.
(function () {
	"use strict";

	// Variant picker - reflect the selected size's stock into the quantity
	// input's max and a live "N left" hint. The server still enforces the real
	// per-variant stock on add-to-cart; this is just a faster signal.
	function wireVariantPickers() {
		var selects = document.querySelectorAll('select[data-testid="variant-select"]');
		Array.prototype.forEach.call(selects, function (select) {
			var form = select.closest("form");
			if (!form) {
				return;
			}
			var quantity = form.querySelector('input[name="quantity"]');
			// The hint lives just outside the form (sibling in the in-stock box),
			// so look in the form's container rather than the form itself.
			var hint = (form.parentNode || form).querySelector("[data-variant-stock-hint]");

			function sync() {
				var option = select.options[select.selectedIndex];
				var stock = option ? parseInt(option.getAttribute("data-stock") || "", 10) : NaN;
				if (!option || !option.value || isNaN(stock)) {
					if (hint) {
						hint.hidden = true;
						hint.textContent = "";
					}
					return;
				}
				if (quantity) {
					quantity.max = String(stock);
					if (parseInt(quantity.value, 10) > stock) {
						quantity.value = String(stock);
					}
				}
				if (hint) {
					hint.textContent = stock === 1 ? "1 left in this size" : stock + " left in this size";
					hint.hidden = false;
				}
			}

			select.addEventListener("change", sync);
			sync();
		});
	}

	// Submit affordance - mark the activated submit control busy so the styled
	// spinner shows during the POST -> redirect round trip. Cleared on the next
	// page load (and on bfcache restore below). We never preventDefault, so the
	// submission proceeds exactly as it would without JS.
	function wireSubmitFeedback() {
		document.addEventListener("submit", function (event) {
			var form = event.target;
			if (!form || form.nodeName !== "FORM") {
				return;
			}
			var submitter = event.submitter;
			if (!submitter || submitter.nodeName !== "BUTTON") {
				submitter = form.querySelector('button[type="submit"], button:not([type])');
			}
			if (submitter) {
				submitter.setAttribute("aria-busy", "true");
			}
		});
	}

	// Restoring a page from the back/forward cache replays the old DOM, which
	// could include a stale busy button. Clear it so the page looks idle.
	window.addEventListener("pageshow", function () {
		var busy = document.querySelectorAll('[aria-busy="true"]');
		Array.prototype.forEach.call(busy, function (el) {
			el.removeAttribute("aria-busy");
		});
	});

	wireVariantPickers();
	wireSubmitFeedback();
})();
