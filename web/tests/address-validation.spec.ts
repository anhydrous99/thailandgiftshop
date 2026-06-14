import { expect, test, type Locator, type Page, type TestInfo } from '@playwright/test';

// Demo mode runs the fake address validator (internal/location.FakeValidator):
// an address whose line 1 contains "force-suggest" comes back Corrected with a
// fixed standardized suggestion (350 Fifth Ave, New York, NY 10118), one
// containing "force-reject" comes back Unverifiable, and anything else is
// Verified. That lets these no-JS specs drive every branch without a network
// call to Amazon Location Service.
//
// Stock budget (INTEGRATION §8): the guest end-to-end test is the only claimant
// of lemongrass-spa-bundle (stock 22) and pays for exactly 1 unit per attempt.
// The address-book tests reserve no stock (they never reach PlaceOrder).
const suggestionStreet = '350 Fifth Ave';
const suggestionCityLine = 'New York, NY 10118';
const password = 'playwright-demo-password';
const guestProductSlug = 'lemongrass-spa-bundle';

function uniqueEmail(testInfo: TestInfo, label: string): string {
  return `${label}-${testInfo.workerIndex}-${Date.now()}@example.test`;
}

async function signUp(page: Page, email: string) {
  await page.goto('/account/sign-up');
  await expect(page.getByTestId('signup-form')).toBeVisible();
  await page.getByLabel('Email').fill(email);
  await page.getByLabel('Password (8 to 72 characters)').fill(password);
  await page.getByRole('button', { name: 'Create account' }).click();
  await expect(page).toHaveURL(/\/account$/);
}

async function fillAddress(form: Locator, line1: string, fullName = 'Anong Shopper') {
  await form.getByLabel('Full name').fill(fullName);
  await form.getByLabel('Address line 1').fill(line1);
  await form.getByLabel('City').fill('Townsville');
  await form.getByLabel('State').selectOption('CA');
  await form.getByLabel('ZIP code').fill('90001');
}

test.describe('without JavaScript', () => {
  test.use({
    javaScriptEnabled: false,
    viewport: { width: 1280, height: 2400 },
  });

  test('address book suggests a standardized address and stores the accepted suggestion', async ({ page }, testInfo) => {
    await signUp(page, uniqueEmail(testInfo, 'addr-suggest'));

    await page.goto('/account/addresses');
    await fillAddress(page.getByTestId('address-form'), '10 force-suggest Blvd');
    await page.getByTestId('address-form').getByRole('button', { name: 'Save address' }).click();

    // The suggestion panel renders and nothing is saved yet.
    const suggestion = page.getByTestId('address-suggestion');
    await expect(suggestion).toBeVisible();
    await expect(suggestion).toContainText(suggestionStreet);
    await expect(page.getByTestId('address-row')).toHaveCount(0);

    // The suggested choice is selected by default; submitting accepts it.
    await page.getByTestId('address-form').getByRole('button', { name: 'Save address' }).click();
    await expect(page).toHaveURL(/\/account\/addresses$/);
    const row = page.getByTestId('address-row');
    await expect(row).toContainText(suggestionStreet);
    await expect(row).toContainText(suggestionCityLine);
  });

  test('address book keeps the entered address when the shopper overrides the suggestion', async ({ page }, testInfo) => {
    await signUp(page, uniqueEmail(testInfo, 'addr-override'));

    await page.goto('/account/addresses');
    await fillAddress(page.getByTestId('address-form'), '10 force-suggest Blvd');
    await page.getByTestId('address-form').getByRole('button', { name: 'Save address' }).click();

    await expect(page.getByTestId('address-suggestion')).toBeVisible();
    await page.getByTestId('address-choice-entered').check();
    await page.getByTestId('address-form').getByRole('button', { name: 'Save address' }).click();

    await expect(page).toHaveURL(/\/account\/addresses$/);
    await expect(page.getByTestId('address-row')).toContainText('10 force-suggest Blvd');
  });

  test('address book blocks an unverifiable address', async ({ page }, testInfo) => {
    await signUp(page, uniqueEmail(testInfo, 'addr-reject'));

    await page.goto('/account/addresses');
    await fillAddress(page.getByTestId('address-form'), '10 force-reject Way');
    await page.getByTestId('address-form').getByRole('button', { name: 'Save address' }).click();

    await expect(page.getByText('We could not verify this as a US shipping address.', { exact: false })).toBeVisible();
    await expect(page.getByTestId('address-row')).toHaveCount(0);
  });

  test('guest checkout surfaces the suggestion before payment and proceeds once accepted', async ({ page }, testInfo) => {
    await page.goto(`/products/${guestProductSlug}`);
    await page.getByRole('button', { name: 'Add to cart' }).click();
    await expect(page).toHaveURL(/\/cart$/);
    await page.getByRole('link', { name: 'Check out', exact: true }).click();
    await expect(page).toHaveURL(/\/checkout$/);

    const form = page.getByTestId('guest-checkout-form');
    await page.getByTestId('guest-email-input').fill(uniqueEmail(testInfo, 'guest-suggest'));
    await fillAddress(form, '10 force-suggest Blvd', 'Guest Shopper');
    await page.getByTestId('place-order-button').click();

    // The order is not placed yet: the suggestion is shown and no stock is held.
    await expect(page.getByTestId('address-suggestion')).toContainText(suggestionStreet);
    await expect(page).not.toHaveURL(/\/fake-pay/);

    // Accepting the suggestion (the default choice) places the order.
    await page.getByTestId('place-order-button').click();
    await expect(page).toHaveURL(/\/checkout\/fake-pay\?session_id=cs_fake_[a-z0-9]{26}$/);
    await page.getByTestId('fake-pay-button').click();
    await expect(page).toHaveURL(/\/orders\/[a-z0-9]{26}\?access=[^&]+&placed=1$/);
  });
});
