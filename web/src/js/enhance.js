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

	function passwordByteLength(value) {
		if (typeof window.TextEncoder === "function") {
			return new window.TextEncoder().encode(value).length;
		}
		try {
			return encodeURIComponent(value).replace(/%[0-9A-F]{2}/g, "x").length;
		} catch (err) {
			return value.length;
		}
	}

	function passwordMinBytes(input) {
		return parseInt(input.getAttribute("data-password-min-bytes") || "", 10) || 0;
	}

	function passwordMaxBytes(input) {
		return parseInt(input.getAttribute("maxlength") || "", 10) || 0;
	}

	function validatePasswordByteLimits(input) {
		var bytes = passwordByteLength(input.value);
		var minBytes = passwordMinBytes(input);
		var maxBytes = passwordMaxBytes(input);
		if (input.value && minBytes && bytes < minBytes) {
			input.setCustomValidity("Use a password at least " + minBytes + " bytes long. Non-ASCII characters may count as more than one byte.");
			return false;
		}
		if (maxBytes && bytes > maxBytes) {
			input.setCustomValidity("Use a password no longer than " + maxBytes + " bytes. Non-ASCII characters may count as more than one byte.");
			return false;
		}
		input.setCustomValidity("");
		return true;
	}

	function wirePasswordByteLimits() {
		var selector = 'input[type="password"][maxlength], input[type="password"][data-password-min-bytes]';
		var inputs = document.querySelectorAll(selector);
		Array.prototype.forEach.call(inputs, function (input) {
			input.addEventListener("input", function () {
				validatePasswordByteLimits(input);
			});
		});

		document.addEventListener("submit", function (event) {
			var form = event.target;
			if (!form || form.nodeName !== "FORM") {
				return;
			}
			var invalid = null;
			var passwordInputs = form.querySelectorAll(selector);
			Array.prototype.forEach.call(passwordInputs, function (input) {
				if (!validatePasswordByteLimits(input) && !invalid) {
					invalid = input;
				}
			});
			if (invalid) {
				event.preventDefault();
				event.stopImmediatePropagation();
				invalid.reportValidity();
				invalid.focus();
			}
		});
	}

	// Submit affordance - mark the activated submit control busy so the styled
	// spinner shows during the POST -> redirect round trip. Cleared on the next
	// page load (and on bfcache restore below). We never preventDefault, so the
	// submission proceeds exactly as it would without JS.
	function wireSubmitFeedback() {
		document.addEventListener("submit", function (event) {
			if (event.defaultPrevented) {
				return;
			}
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

	// Confirmation gate for destructive actions (clear cart, remove address /
	// card). A form opts in with data-confirm="message" (and optional
	// data-confirm-title / data-confirm-label). We hold the first submit, ask
	// for confirmation through the shared <dialog>, and only let the real POST
	// through once accepted. With no JS - or no <dialog> support - the form
	// posts exactly as before, so this is an additive guardrail, never a gate;
	// the server-side origin/CSRF checks remain the real protection.
	function wireDestructiveConfirms() {
		var dialog = document.querySelector("dialog[data-confirm-dialog]");
		var supportsDialog = !!dialog && typeof dialog.showModal === "function";
		var titleEl = supportsDialog ? dialog.querySelector("[data-confirm-dialog-title]") : null;
		var messageEl = supportsDialog ? dialog.querySelector("[data-confirm-dialog-message]") : null;
		var acceptEl = supportsDialog ? dialog.querySelector("[data-confirm-accept]") : null;
		var cancelEl = supportsDialog ? dialog.querySelector("[data-confirm-cancel]") : null;
		// pending holds the submit we are deferring: { form, submitter }.
		var pending = null;

		function submitConfirmed(form, submitter) {
			form.setAttribute("data-confirmed", "true");
			try {
				if (typeof form.requestSubmit === "function") {
					if (submitter && submitter.type === "submit" && submitter.form === form) {
						form.requestSubmit(submitter);
					} else {
						form.requestSubmit();
					}
					return;
				}
			} catch (err) {
				// Fall through to the plain submit below.
			}
			form.submit();
		}

		if (supportsDialog) {
			if (acceptEl) {
				acceptEl.addEventListener("click", function () {
					var current = pending;
					pending = null; // Prevent the close handler treating this as a cancel.
					dialog.close();
					if (current) {
						submitConfirmed(current.form, current.submitter);
					}
				});
			}
			if (cancelEl) {
				cancelEl.addEventListener("click", function () {
					dialog.close();
				});
			}
			// Esc, the Cancel button, or a backdrop click all fire "close"; treat
			// any close with a pending submit as a cancellation and restore focus
			// to the trigger.
			dialog.addEventListener("close", function () {
				if (!pending) {
					return;
				}
				var trigger = pending.submitter;
				pending = null;
				if (trigger && typeof trigger.focus === "function") {
					trigger.focus();
				}
			});
			// A click on the dialog element itself (not its contents) is the
			// backdrop; dismiss like Cancel.
			dialog.addEventListener("click", function (event) {
				if (event.target === dialog && dialog.open) {
					dialog.close();
				}
			});
		}

		document.addEventListener("submit", function (event) {
			var form = event.target;
			if (!form || form.nodeName !== "FORM") {
				return;
			}
			var message = form.getAttribute("data-confirm");
			if (message === null) {
				return;
			}
			// Second pass: confirmation already granted, let the POST proceed and
			// let the other submit enhancers (spinner) run normally.
			if (form.getAttribute("data-confirmed") === "true") {
				form.removeAttribute("data-confirmed");
				return;
			}

			var submitter = event.submitter;
			if (!submitter || submitter.nodeName !== "BUTTON") {
				submitter = form.querySelector('button[type="submit"], button:not([type])');
			}

			// No modal available: fall back to the native confirm() so a click
			// still gets a yes/no step.
			if (!supportsDialog) {
				if (!window.confirm(message)) {
					event.preventDefault();
					event.stopImmediatePropagation();
				}
				return;
			}

			event.preventDefault();
			event.stopImmediatePropagation();
			pending = { form: form, submitter: submitter };
			if (titleEl) {
				titleEl.textContent = form.getAttribute("data-confirm-title") || "Are you sure?";
			}
			if (messageEl) {
				messageEl.textContent = message;
			}
			if (acceptEl) {
				acceptEl.textContent = form.getAttribute("data-confirm-label") || "Confirm";
			}
			dialog.showModal();
			if (cancelEl && typeof cancelEl.focus === "function") {
				cancelEl.focus();
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

	// Register the confirmation gate first so its submit listener runs before
	// the password validator and the busy-spinner affordance: holding a submit
	// here keeps those from firing on a deferred or cancelled action.
	wireDestructiveConfirms();
	wireVariantPickers();
	wirePasswordByteLimits();
	wireSubmitFeedback();
})();
