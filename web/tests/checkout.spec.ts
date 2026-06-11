import { expect, test, type Page, type TestInfo } from '@playwright/test';

const password = 'playwright-demo-password';

function uniqueEmail(testInfo: TestInfo, label: string): string {
  return `${label}-${testInfo.workerIndex}-${Date.now()}@example.test`;
}

async function signUp(page: Page, email: string, returnTo?: string) {
  const path = returnTo
    ? `/account/sign-up?return_to=${encodeURIComponent(returnTo)}`
    : '/account/sign-up';
  await page.goto(path);
  await expect(page.getByTestId('signup-form')).toBeVisible();
  await page.getByLabel('Email').fill(email);
  await page.getByLabel('Password (8 to 72 characters)').fill(password);
  await page.getByRole('button', { name: 'Create account' }).click();
}

async function saveAddress(page: Page, fullName: string) {
  const form = page.getByTestId('address-form');
  await form.getByLabel('Full name').fill(fullName);
  await form.getByLabel('Address line 1').fill('44 Rama IV Road');
  await form.getByLabel('City').fill('Austin');
  await form.getByLabel('State').fill('TX');
  await form.getByLabel('ZIP code').fill('78701');
  await form.getByRole('button', { name: 'Save address' }).click();
}

async function addProductToCart(page: Page, slug: string, options: { quantity?: string; variantLabel?: string } = {}) {
  await page.goto(`/products/${slug}`);
  if (options.variantLabel) {
    await page.getByTestId('variant-select').selectOption({ label: options.variantLabel });
  }
  if (options.quantity) {
    await page.getByLabel('Quantity').fill(options.quantity);
  }
  await page.getByRole('button', { name: 'Add to cart' }).click();
  await expect(page).toHaveURL(/\/cart$/);
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

  test('anonymous cart hands off to sign-in, sign-up merges the cart, and the paid order lands in history', async ({ page }, testInfo) => {
    const email = uniqueEmail(testInfo, 'buyer');

    // Anonymous shopper builds a cart.
    await addProductToCart(page, 'mango-sticky-rice-kit', { quantity: '2' });
    await expect(page.getByRole('link', { name: 'Cart (2)', exact: true }).first()).toBeVisible();

    // Checkout now renders the guest layout for anonymous shoppers; the
    // sign-in link reaches the account flow with a validated return_to.
    await page.getByRole('link', { name: 'Check out', exact: true }).click();
    await expect(page).toHaveURL(/\/checkout$/);
    await expect(page.getByTestId('guest-checkout-form')).toBeVisible();
    await page.getByTestId('checkout-sign-in-link').click();
    await expect(page).toHaveURL(/\/account\/sign-in\?return_to=%2Fcheckout$/);
    await expect(page.getByTestId('signin-form')).toBeVisible();

    // Signing up returns to checkout and the anonymous cart merges into the account.
    await signUp(page, email, '/checkout');
    await expect(page).toHaveURL(/\/checkout$/);
    await expect(page.getByRole('link', { name: 'Cart (2)', exact: true }).first()).toBeVisible();

    // With no saved address the page asks for one before offering payment.
    await expect(page.getByRole('heading', { name: 'Add an address to continue' })).toBeVisible();
    await expect(page.getByText('Save a shipping address to continue to payment.')).toBeVisible();
    await expect(page.getByTestId('place-order-button')).toHaveCount(0);

    // Saving the inline address returns to checkout with the option selected.
    await saveAddress(page, 'Mango Buyer');
    await expect(page).toHaveURL(/\/checkout$/);
    const addressOption = page.getByTestId('checkout-address-option').filter({ hasText: 'Mango Buyer' });
    await expect(addressOption).toBeVisible();
    await expect(addressOption.locator('input[name="address_id"]')).toBeChecked();

    // The priced summary comes from the server cart.
    const checkoutLine = page.getByTestId('checkout-line-item').filter({ hasText: 'Mango Sticky Rice Treats' });
    await expect(checkoutLine).toBeVisible();
    await expect(checkoutLine.getByText('Quantity 2 at')).toBeVisible();
    await expect(checkoutLine.getByText('Line total $57.98')).toBeVisible();
    await expect(page.getByText('You enter card details on Stripe\'s secure page.')).toBeVisible();

    // Placing the order redirects to the demo payment page showing the total.
    await page.getByTestId('place-order-button').click();
    await expect(page).toHaveURL(/\/checkout\/fake-pay\?session_id=cs_fake_[a-z0-9]{26}$/);
    await expect(page.getByRole('heading', { name: 'Pay $57.98', level: 1 })).toBeVisible();
    await expect(page.getByText('Demo payment — no real charge')).toBeVisible();

    // Paying finalizes through the real confirm path onto the order page.
    await page.getByTestId('fake-pay-button').click();
    await expect(page).toHaveURL(/\/orders\/[a-z0-9]{26}\?placed=1$/);
    const orderID = new URL(page.url()).pathname.split('/').pop() as string;
    await expect(page.getByTestId('order-placed-banner')).toBeVisible();
    await expect(page.getByTestId('order-status')).toHaveText('Paid');
    await expect(page.getByTestId('order-line-item').filter({ hasText: 'Mango Sticky Rice Treats' })).toBeVisible();
    await expect(page.getByTestId('order-timeline-step')).toHaveCount(2);

    // The order shows up in the history list.
    await page.goto('/orders');
    await expect(page.getByTestId('order-row').filter({ hasText: orderID })).toBeVisible();

    // Payment success cleared the cart and reset the header label.
    await page.goto('/cart');
    await expect(page.getByText('Your cart is empty.')).toBeVisible();
    await expect(page.getByRole('link', { name: 'Cart', exact: true }).first()).toBeVisible();
    await expect(page.getByRole('link', { name: /Cart \(\d+\)/ })).toHaveCount(0);
  });

  test('canceling on the payment page returns to checkout with the cart intact', async ({ page }, testInfo) => {
    const email = uniqueEmail(testInfo, 'canceler');
    await signUp(page, email);
    await expect(page).toHaveURL(/\/account$/);

    await page.goto('/account/addresses');
    await saveAddress(page, 'Cancel Tester');
    await expect(page).toHaveURL(/\/account\/addresses$/);

    await addProductToCart(page, 'ceramic-tuk-tuk-magnet-set');
    await page.getByRole('link', { name: 'Check out', exact: true }).click();
    await expect(page).toHaveURL(/\/checkout$/);
    await page.getByTestId('place-order-button').click();
    await expect(page).toHaveURL(/\/checkout\/fake-pay\?session_id=cs_fake_[a-z0-9]{26}$/);

    await page.getByTestId('fake-pay-cancel').click();
    await expect(page).toHaveURL(/\/checkout\?canceled=1$/);
    await expect(page.getByTestId('checkout-canceled-notice')).toContainText('Your cart is unchanged');
    await expect(page.getByTestId('checkout-line-item').filter({ hasText: 'Ceramic Tuk Tuk Magnets' })).toBeVisible();
    await expect(page.getByRole('link', { name: 'Cart (1)', exact: true }).first()).toBeVisible();

    await page.goto('/cart');
    await expect(page.getByTestId('cart-line-item').filter({ hasText: 'Ceramic Tuk Tuk Magnets' })).toBeVisible();
  });

  test('a canceled last-unit reservation is released by the pending-order cleanup and is purchasable again', async ({ page }, testInfo) => {
    const email = uniqueEmail(testInfo, 'lastunit');
    await signUp(page, email);
    await expect(page).toHaveURL(/\/account$/);

    await page.goto('/account/addresses');
    await saveAddress(page, 'Last Unit Tester');
    await expect(page).toHaveURL(/\/account\/addresses$/);

    // The demo scarf has exactly one S in stock; placing the order reserves it.
    await addProductToCart(page, 'handwoven-indigo-scarf', { variantLabel: 'S' });
    await page.getByRole('link', { name: 'Check out', exact: true }).click();
    await expect(page).toHaveURL(/\/checkout$/);
    await page.getByTestId('place-order-button').click();
    await expect(page).toHaveURL(/\/checkout\/fake-pay\?session_id=cs_fake_[a-z0-9]{26}$/);
    await expect(page.getByRole('heading', { name: 'Pay $45.99', level: 1 })).toBeVisible();

    // Canceling keeps the reservation: checkout sees the now-unavailable size
    // and falls back to the cart, where the line survives with a notice.
    await page.getByTestId('fake-pay-cancel').click();
    await expect(page).toHaveURL(/\/cart$/);
    await expect(page.getByTestId('cart-line-item').filter({ hasText: 'Handwoven Indigo Scarf' })).toBeVisible();
    await expect(page.getByText('Selected size is unavailable. Remove it to continue.')).toBeVisible();

    // The product page confirms the last unit is still reserved.
    await page.goto('/products/handwoven-indigo-scarf');
    await expect(page.getByTestId('variant-select').locator('option[value="var_005_s"]')).toHaveText('S - out of stock');

    // Replacing the cart and placing a new order triggers the stale-pointer
    // cleanup: the old pending order is canceled and its stock released.
    await page.goto('/cart');
    await page.getByRole('button', { name: 'Remove' }).click();
    await expect(page.getByText('Your cart is empty.')).toBeVisible();
    await addProductToCart(page, 'thai-tea-sampler');
    await page.getByRole('link', { name: 'Check out', exact: true }).click();
    await expect(page).toHaveURL(/\/checkout$/);
    await page.getByTestId('place-order-button').click();
    await expect(page).toHaveURL(/\/checkout\/fake-pay\?session_id=cs_fake_[a-z0-9]{26}$/);
    const fakePayURL = page.url();

    // The scarf's last unit is back on the shelf.
    await page.goto('/products/handwoven-indigo-scarf');
    await expect(page.getByTestId('variant-select').locator('option[value="var_005_s"]')).toHaveText('S');

    // Finish the replacement order cleanly.
    await page.goto(fakePayURL);
    await page.getByTestId('fake-pay-button').click();
    await expect(page).toHaveURL(/\/orders\/[a-z0-9]{26}\?placed=1$/);
    await expect(page.getByTestId('order-status')).toHaveText('Paid');
    await expect(page.getByTestId('order-line-item').filter({ hasText: 'Thai Tea Selection' })).toBeVisible();
  });
});

test('checkout renders the guest layout with JavaScript enabled', async ({ page }) => {
  await addProductToCart(page, 'jasmine-rice-candle');
  await page.goto('/checkout');
  await expect(page).toHaveURL(/\/checkout$/);
  await expect(page.getByTestId('guest-checkout-form')).toBeVisible();
  await expect(page.getByTestId('checkout-sign-in-link')).toBeVisible();
});
