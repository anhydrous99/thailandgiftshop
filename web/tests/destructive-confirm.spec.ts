import { expect, test, type Page, type TestInfo } from '@playwright/test';

// These tests run with JavaScript enabled (the default), so they exercise the
// progressive-enhancement confirmation gate added in enhance.js: destructive
// forms (clear cart, remove address, remove card) open a shared <dialog> and
// only post once confirmed. The matching `without JavaScript` specs in
// account.spec.ts and payment-methods.spec.ts cover the no-JS fallback where the
// same forms post directly.

const password = 'playwright-demo-password';

function uniqueEmail(testInfo: TestInfo, label: string): string {
  return `${label}-${testInfo.workerIndex}-${Date.now()}@example.test`;
}

async function signUp(page: Page, email: string) {
  await page.goto('/account/sign-up');
  await expect(page.getByTestId('signup-form')).toBeVisible();
  await page.getByLabel('Email').fill(email);
  await page.getByLabel('Password (8 to 72 bytes)').fill(password);
  await page.getByRole('button', { name: 'Create account' }).click();
  await expect(page).toHaveURL(/\/account$/);
}

async function addCard(page: Page) {
  await page.goto('/account/payment-methods');
  await page.getByRole('button', { name: 'Add a card' }).click();
  await expect(page).toHaveURL(/\/checkout\/fake-pay\?session_id=cs_fake_setup_[a-z0-9]{26}$/);
  await page.getByTestId('fake-pay-button').click();
  await expect(page).toHaveURL(/\/account\/payment-methods\?saved=1$/);
}

test('clear cart asks for confirmation: cancel keeps items, confirm empties', async ({ page }) => {
  await page.goto('/products/mango-sticky-rice-kit');
  await page.getByRole('button', { name: 'Add to cart' }).click();
  await expect(page).toHaveURL(/\/cart$/);
  const cartLine = page.getByTestId('cart-line-item').filter({ hasText: 'Mango Sticky Rice Treats' });
  await expect(cartLine).toBeVisible();

  // First click opens the dialog and holds the POST - the cart is untouched.
  await page.getByRole('button', { name: 'Clear cart' }).click();
  const dialog = page.getByRole('dialog');
  await expect(dialog).toBeVisible();
  await expect(dialog).toContainText("Remove all items from your cart? This can't be undone.");
  await expect(cartLine).toBeVisible();

  // Escape dismisses without clearing.
  await page.keyboard.press('Escape');
  await expect(dialog).toBeHidden();
  await expect(cartLine).toBeVisible();

  // Reopen and cancel via the button - still no POST.
  await page.getByRole('button', { name: 'Clear cart' }).click();
  await expect(dialog).toBeVisible();
  await dialog.getByRole('button', { name: 'Cancel' }).click();
  await expect(dialog).toBeHidden();
  await expect(cartLine).toBeVisible();

  // Confirm actually clears the cart.
  await page.getByRole('button', { name: 'Clear cart' }).click();
  await expect(dialog).toBeVisible();
  await dialog.getByRole('button', { name: 'Clear cart' }).click();
  await expect(page).toHaveURL(/\/cart$/);
  await expect(page.getByText('Your cart is empty.')).toBeVisible();
  await expect(page.getByTestId('cart-line-item')).toHaveCount(0);
});

test('removing an address asks for confirmation: cancel keeps it, confirm removes', async ({ page }, testInfo) => {
  const email = uniqueEmail(testInfo, 'confirm-address');
  await signUp(page, email);

  await page.goto('/account/addresses');
  const form = page.getByTestId('address-form');
  await form.getByLabel('Full name').fill('Confirm Shopper');
  await form.getByLabel('Address line 1').fill('44 Silom Road');
  await form.getByLabel('City').fill('Austin');
  await form.getByLabel('State').selectOption('TX');
  await form.getByLabel('ZIP code').fill('78701');
  await page.getByRole('button', { name: 'Save address' }).click();

  const row = page.getByTestId('address-row').filter({ hasText: 'Confirm Shopper' });
  await expect(row).toBeVisible();

  // Cancel leaves the address in place.
  await row.getByRole('button', { name: 'Remove' }).click();
  const dialog = page.getByRole('dialog');
  await expect(dialog).toBeVisible();
  await expect(dialog).toContainText("Remove this address? This can't be undone.");
  await dialog.getByRole('button', { name: 'Cancel' }).click();
  await expect(dialog).toBeHidden();
  await expect(row).toBeVisible();

  // Confirm removes it.
  await row.getByRole('button', { name: 'Remove' }).click();
  await expect(dialog).toBeVisible();
  await dialog.getByRole('button', { name: 'Remove address' }).click();
  await expect(page).toHaveURL(/\/account\/addresses$/);
  await expect(page.getByTestId('address-row')).toHaveCount(0);
});

test('removing a saved card asks for confirmation: cancel keeps it, confirm removes', async ({ page }, testInfo) => {
  const email = uniqueEmail(testInfo, 'confirm-card');
  await signUp(page, email);
  await addCard(page);

  const cardRow = page.getByTestId('payment-method-row').filter({ hasText: '4242' });
  await expect(cardRow).toBeVisible();

  // Cancel leaves the card saved.
  await cardRow.getByRole('button', { name: 'Remove' }).click();
  const dialog = page.getByRole('dialog');
  await expect(dialog).toBeVisible();
  await expect(dialog).toContainText('Remove this card? You can add it again later.');
  await dialog.getByRole('button', { name: 'Cancel' }).click();
  await expect(dialog).toBeHidden();
  await expect(cardRow).toBeVisible();

  // Confirm removes it.
  await cardRow.getByRole('button', { name: 'Remove' }).click();
  await expect(dialog).toBeVisible();
  await dialog.getByRole('button', { name: 'Remove card' }).click();
  await expect(page).toHaveURL(/\/account\/payment-methods$/);
  await expect(page.getByTestId('payment-method-row')).toHaveCount(0);
  await expect(page.getByText('No saved cards yet.')).toBeVisible();
});
