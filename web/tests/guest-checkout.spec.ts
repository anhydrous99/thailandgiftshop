import { expect, test, type Page, type TestInfo } from '@playwright/test';

// Stock budget (INTEGRATION §8): this spec is the only claimant of
// coconut-curry-pantry-box (stock 25, no variants). Worst case per attempt:
// 4 paid units plus at most 1 stranded abandoned reservation (signed cancel
// returns release their own reservation even in demo mode); a full retry
// doubles that to 10, still comfortably under 25. Never draw
// from ceramic-tuk-tuk-magnet-set, thai-tea-sampler, or elephant-pouch-set
// (orders-paging.spec.ts stock budget), jasmine-rice-candle
// (checkout.spec.ts), or handwoven-indigo-scarf size S (last-unit test).
const slug = 'coconut-curry-pantry-box';
const productName = 'Coconut Curry Pantry Picks';
const adminPassword = 'admin-product-password';

function uniqueEmail(testInfo: TestInfo, label: string): string {
  return `${label}-${testInfo.workerIndex}-${Date.now()}@example.test`;
}

async function addProductToCart(page: Page) {
  await page.goto(`/products/${slug}`);
  await page.getByRole('button', { name: 'Add to cart' }).click();
  await expect(page).toHaveURL(/\/cart$/);
}

async function fillGuestCheckoutForm(page: Page, email: string) {
  const form = page.getByTestId('guest-checkout-form');
  await expect(form).toBeVisible();
  await page.getByTestId('guest-email-input').fill(email);
  await form.getByLabel('Full name').fill('Guest Shopper');
  await form.getByLabel('Address line 1').fill('77 Yaowarat Road');
  await form.getByLabel('City').fill('Dallas');
  await form.getByLabel('State').selectOption('TX');
  await form.getByLabel('ZIP code').fill('75201');
}

// placeGuestOrder drives cart → guest checkout → fake-pay and returns the
// fake session id from the fake-pay URL.
async function placeGuestOrder(page: Page, email: string): Promise<string> {
  await addProductToCart(page);
  await page.getByRole('link', { name: 'Check out', exact: true }).click();
  await expect(page).toHaveURL(/\/checkout$/);
  await fillGuestCheckoutForm(page, email);
  await page.getByTestId('place-order-button').click();
  await expect(page).toHaveURL(/\/checkout\/fake-pay\?session_id=cs_fake_[a-z0-9]{26}$/);
  return new URL(page.url()).searchParams.get('session_id') as string;
}

// payGuestOrder completes fake-pay and lands on the tokenized order page,
// returning its full URL.
async function payGuestOrder(page: Page): Promise<string> {
  await page.getByTestId('fake-pay-button').click();
  await expect(page).toHaveURL(/\/orders\/[a-z0-9]{26}\?access=[^&]+&placed=1$/);
  return page.url();
}

test.describe('without JavaScript', () => {
  test.use({
    javaScriptEnabled: false,
    // The site shell sets scroll-smooth on <html>; with page JavaScript
    // disabled, Playwright's scroll-into-view animation for below-the-fold
    // controls never settles, so use a viewport tall enough to avoid
    // scrolling altogether.
    viewport: { width: 1280, height: 2400 },
  });

  test('guest checks out end-to-end without an account', async ({ page }, testInfo) => {
    const email = uniqueEmail(testInfo, 'guest');

    await addProductToCart(page);
    await page.getByRole('link', { name: 'Check out', exact: true }).click();

    // No sign-in redirect: the guest layout renders on /checkout itself.
    await expect(page).toHaveURL(/\/checkout$/);
    await expect(page.getByTestId('guest-checkout-form')).toBeVisible();
    await expect(page.getByTestId('guest-email-input')).toBeVisible();
    await expect(page.getByTestId('checkout-sign-in-link')).toBeVisible();
    const checkoutLine = page.getByTestId('checkout-line-item').filter({ hasText: productName });
    await expect(checkoutLine).toBeVisible();

    await fillGuestCheckoutForm(page, email);
    await page.getByTestId('place-order-button').click();
    await expect(page).toHaveURL(/\/checkout\/fake-pay\?session_id=cs_fake_[a-z0-9]{26}$/);

    // Guests never get the save-card option (no account to save to).
    await expect(page.locator('input[name="save_card"]')).toHaveCount(0);
    await expect(page.getByRole('heading', { name: 'Pay $32.99', level: 1 })).toBeVisible();

    await page.getByTestId('fake-pay-button').click();
    await expect(page).toHaveURL(/\/orders\/[a-z0-9]{26}\?access=[^&]+&placed=1$/);
    await expect(page.getByTestId('order-status')).toHaveText('Paid');
    await expect(page.getByTestId('order-placed-banner')).toBeVisible();
    await expect(page.getByTestId('guest-order-link-notice')).toBeVisible();
    await expect(page.getByTestId('guest-signup-upsell')).toBeVisible();
    await expect(page.getByTestId('order-line-item').filter({ hasText: productName })).toBeVisible();

    // Payment cleared the cookie cart and reset the header label.
    await expect(page.getByRole('link', { name: 'Cart', exact: true }).first()).toBeVisible();
    await expect(page.getByRole('link', { name: /Cart \(\d+\)/ })).toHaveCount(0);
  });

  test('the access link survives a cookie wipe and the bare URL stays gated', async ({ page, context }, testInfo) => {
    const email = uniqueEmail(testInfo, 'guest-wipe');
    await placeGuestOrder(page, email);
    const tokenizedURL = await payGuestOrder(page);
    const orderID = new URL(tokenizedURL).pathname.split('/').pop() as string;

    // The tokenized link is the credential: cookies are irrelevant.
    await context.clearCookies();
    await page.goto(tokenizedURL);
    await expect(page.getByTestId('order-status')).toHaveText('Paid');
    await expect(page.getByTestId('guest-order-link-notice')).toBeVisible();

    // Without the token the page is sign-in gated like any order page.
    await page.goto(`/orders/${orderID}`);
    await expect(page).toHaveURL(new RegExp(`/account/sign-in\\?return_to=%2Forders%2F${orderID}$`));
  });

  test('a tampered access token falls back to the anonymous sign-in redirect', async ({ page }, testInfo) => {
    const email = uniqueEmail(testInfo, 'guest-tamper');
    await placeGuestOrder(page, email);
    const tokenizedURL = new URL(await payGuestOrder(page));
    const orderID = tokenizedURL.pathname.split('/').pop() as string;
    const token = tokenizedURL.searchParams.get('access') as string;

    await page.goto(`/orders/${orderID}?access=${token}x`);
    await expect(page).toHaveURL(new RegExp(`/account/sign-in\\?return_to=%2Forders%2F${orderID}$`));
  });

  test('canceling the demo payment keeps the guest cart', async ({ page }, testInfo) => {
    const email = uniqueEmail(testInfo, 'guest-cancel');
    await placeGuestOrder(page, email);

    await page.getByTestId('fake-pay-cancel').click();
    await expect(page).toHaveURL(/\/checkout\?canceled=1&cancel_token=[A-Za-z0-9_-]+(?:\.[A-Za-z0-9_-]+)?$/);
    await expect(page.getByTestId('checkout-canceled-notice')).toContainText('Your cart is unchanged');
    await expect(page.getByTestId('guest-checkout-form')).toBeVisible();

    await page.goto('/cart');
    await expect(page.getByTestId('cart-line-item').filter({ hasText: productName })).toBeVisible();
  });

  test('resubmitting the same cart address and email resumes the session', async ({ page }, testInfo) => {
    const email = uniqueEmail(testInfo, 'guest-resume');
    const sessionID = await placeGuestOrder(page, email);

    // Back on checkout with the identical cart, address, and email, the
    // signed pointer cookie resumes the pending order instead of duplicating.
    await page.goto('/checkout');
    await fillGuestCheckoutForm(page, email);
    await page.getByTestId('place-order-button').click();
    await expect(page).toHaveURL(/\/checkout\/fake-pay\?session_id=cs_fake_[a-z0-9]{26}$/);
    expect(new URL(page.url()).searchParams.get('session_id')).toBe(sessionID);
  });
});

test('admin sees the guest order with the guest chip', async ({ page }, testInfo) => {
  const email = uniqueEmail(testInfo, 'guest-admin');
  await placeGuestOrder(page, email);
  await payGuestOrder(page);

  await page.goto('/admin/login');
  await page.getByLabel('Password').fill(adminPassword);
  await page.getByRole('button', { name: 'Sign in' }).click();
  await expect(page).toHaveURL(/\/admin$/);

  await page.goto('/admin/orders');
  const row = page.getByTestId('admin-order-row').filter({ hasText: email });
  await expect(row).toBeVisible();
  await expect(row.getByText('· Guest')).toBeVisible();

  await row.getByRole('link', { name: 'Review' }).click();
  await expect(page.getByTestId('admin-order-guest')).toHaveText('Guest checkout');
  await expect(page.getByTestId('admin-order-status')).toHaveText('Paid');
  await expect(page.getByTestId('admin-order-payment-intent')).toContainText('pi_fake_');
});
