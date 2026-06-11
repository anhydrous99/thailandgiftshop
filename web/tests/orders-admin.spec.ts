import { expect, test, type Page, type TestInfo } from '@playwright/test';

const adminPassword = 'admin-product-password';
const password = 'playwright-demo-password';

function uniqueEmail(testInfo: TestInfo, label: string): string {
  return `${label}-${testInfo.workerIndex}-${Date.now()}@example.test`;
}

async function adminLogin(page: Page) {
  await page.goto('/admin/login');
  await page.getByLabel('Password').fill(adminPassword);
  await page.getByRole('button', { name: 'Sign in' }).click();
  await expect(page).toHaveURL(/\/admin$/);
  await expect(page.getByTestId('admin-nav')).toBeVisible();
}

async function placePaidOrder(page: Page, email: string): Promise<string> {
  await page.goto('/account/sign-up');
  await page.getByLabel('Email').fill(email);
  await page.getByLabel('Password (8 to 72 characters)').fill(password);
  await page.getByRole('button', { name: 'Create account' }).click();
  await expect(page).toHaveURL(/\/account$/);

  await page.goto('/account/addresses');
  const form = page.getByTestId('address-form');
  await form.getByLabel('Full name').fill('Order Desk Tester');
  await form.getByLabel('Address line 1').fill('55 Silom Road');
  await form.getByLabel('City').fill('Houston');
  await form.getByLabel('State').fill('TX');
  await form.getByLabel('ZIP code').fill('77002');
  await form.getByRole('button', { name: 'Save address' }).click();
  await expect(page).toHaveURL(/\/account\/addresses$/);

  await page.goto('/products/elephant-pouch-set');
  await page.getByRole('button', { name: 'Add to cart' }).click();
  await expect(page).toHaveURL(/\/cart$/);
  await page.getByRole('link', { name: 'Check out', exact: true }).click();
  await expect(page).toHaveURL(/\/checkout$/);
  await page.getByTestId('place-order-button').click();
  await expect(page).toHaveURL(/\/checkout\/fake-pay\?session_id=cs_fake_[a-z0-9]{26}$/);
  await page.getByTestId('fake-pay-button').click();
  await expect(page).toHaveURL(/\/orders\/[a-z0-9]{26}\?placed=1$/);

  return new URL(page.url()).pathname.split('/').pop() as string;
}

test('admin order desk advances a paid order through tracking shipping and delivery', async ({ page }, testInfo) => {
  const email = uniqueEmail(testInfo, 'orderdesk');
  const orderID = await placePaidOrder(page, email);

  await adminLogin(page);

  // The paid order shows up in the order desk list.
  await page.goto('/admin/orders');
  const row = page.getByTestId('admin-order-row').filter({ hasText: orderID });
  await expect(row).toBeVisible();
  await expect(row.getByTestId('admin-order-status')).toHaveText('Paid');
  await expect(row.getByText(email)).toBeVisible();

  // The detail page offers the tracking form for a paid order.
  await row.getByRole('link', { name: 'Review' }).click();
  await expect(page).toHaveURL(new RegExp(`/admin/orders/${orderID}$`));
  await expect(page.getByTestId('admin-order-status')).toHaveText('Paid');
  await expect(page.getByTestId('admin-order-payment-intent')).toContainText('pi_fake_');

  // Saving tracking transitions the order to shipped.
  await page.getByTestId('admin-order-carrier-input').fill('USPS');
  await page.getByTestId('admin-order-tracking-input').fill('9400-1234-5678-90');
  await page.getByTestId('admin-order-tracking-button').click();
  await expect(page).toHaveURL(new RegExp(`/admin/orders/${orderID}\\?saved=shipped$`));
  await expect(page.getByTestId('admin-flash')).toContainText('Tracking saved and order marked shipped.');
  await expect(page.getByTestId('admin-order-status')).toHaveText('Shipped');
  await expect(page.getByTestId('admin-order-tracking-value')).toHaveText('USPS 9400-1234-5678-90');

  // Advancing moves shipped to delivered through the transition map.
  await page.getByTestId('admin-order-advance-status').selectOption('delivered');
  await page.getByTestId('admin-order-advance-button').click();
  await expect(page).toHaveURL(new RegExp(`/admin/orders/${orderID}\\?saved=advanced$`));
  await expect(page.getByTestId('admin-order-status')).toHaveText('Delivered');

  // The shopper's order page shows the full timeline and the tracking details.
  await page.goto(`/orders/${orderID}`);
  await expect(page.getByTestId('order-status')).toHaveText('Delivered');
  await expect(page.getByTestId('order-tracking')).toHaveText('USPS 9400-1234-5678-90');
  const steps = page.getByTestId('order-timeline-step');
  await expect(steps).toHaveCount(4);
  await expect(steps.nth(0)).toContainText('Pending payment');
  await expect(steps.nth(1)).toContainText('Paid');
  await expect(steps.nth(2)).toContainText('Shipped');
  await expect(steps.nth(3)).toContainText('Delivered');
});

test('admin refunds a paid order and the shopper sees it refunded', async ({ page }, testInfo) => {
  // Draws one transient elephant-pouch-set unit, returned to stock when the
  // refund settles (parallel-safe against the advance test's one paid unit).
  const email = uniqueEmail(testInfo, 'refunddesk');
  const orderID = await placePaidOrder(page, email);

  await adminLogin(page);

  // The paid order's detail page offers the refund action.
  await page.goto(`/admin/orders/${orderID}`);
  await expect(page.getByTestId('admin-order-status')).toHaveText('Paid');
  await expect(page.getByTestId('admin-order-refund')).toBeVisible();
  await page.getByTestId('admin-order-refund-button').click();

  // The demo provider settles synchronously: issued and confirmed in one step.
  await expect(page).toHaveURL(/\?saved=refunded$/);
  await expect(page.getByTestId('admin-flash')).toContainText('Refund issued and confirmed.');
  await expect(page.getByTestId('admin-order-status')).toHaveText('Refunded');
  await expect(page.getByTestId('admin-order-refund-id')).toHaveText(/re_fake_[a-z0-9]{26}_1/);
  await expect(page.getByTestId('admin-order-refund')).toHaveCount(0);
  await expect(page.getByTestId('admin-order-cancel')).toHaveCount(0);
  await expect(page.getByTestId('admin-order-refund-failed')).toHaveCount(0);

  // The status history records both refund steps.
  const history = page.getByTestId('admin-order-history');
  await expect(history).toContainText('Refund pending');
  await expect(history).toContainText('Refunded');

  // The shopper's order page renders the refund status and full timeline.
  await page.goto(`/orders/${orderID}`);
  await expect(page.getByTestId('order-status')).toHaveText('Refunded');
  const steps = page.getByTestId('order-timeline-step');
  await expect(steps).toHaveCount(4);
  await expect(steps.nth(0)).toContainText('Pending payment');
  await expect(steps.nth(1)).toContainText('Paid');
  await expect(steps.nth(2)).toContainText('Refund pending');
  await expect(steps.nth(3)).toContainText('Refunded');
});
