import { expect, test, type Page, type TestInfo } from '@playwright/test';

// Stock budget: this spec is the only claimant of teak-elephant-carving
// (stock 9, no variants). It pays for exactly 1 guest unit per attempt; a full
// retry doubles that to 2, comfortably under 9. Never draw from
// ceramic-tuk-tuk-magnet-set, thai-tea-sampler, or elephant-pouch-set
// (orders-paging.spec.ts), coconut-curry-pantry-box (guest-checkout.spec.ts),
// jasmine-rice-candle (checkout.spec.ts), lemongrass-spa-bundle
// (address-validation.spec.ts), or handwoven-indigo-scarf size S.
const slug = 'teak-elephant-carving';
const productName = 'Teak Elephant Carving';
const adminPassword = 'admin-product-password';

function uniqueEmail(testInfo: TestInfo, label: string): string {
  return `${label}-${testInfo.workerIndex}-${Date.now()}@example.test`;
}

// placeGuestOrder drives cart → guest checkout → fake-pay so the analytics
// window has at least one paid order with a known product line.
async function placeGuestOrder(page: Page, email: string) {
  await page.goto(`/products/${slug}`);
  await page.getByRole('button', { name: 'Add to cart' }).click();
  await expect(page).toHaveURL(/\/cart$/);

  await page.getByRole('link', { name: 'Check out', exact: true }).click();
  await expect(page).toHaveURL(/\/checkout$/);

  const form = page.getByTestId('guest-checkout-form');
  await expect(form).toBeVisible();
  await page.getByTestId('guest-email-input').fill(email);
  await form.getByLabel('Full name').fill('Analytics Tester');
  await form.getByLabel('Address line 1').fill('88 Charoen Krung Road');
  await form.getByLabel('City').fill('Dallas');
  await form.getByLabel('State').selectOption('TX');
  await form.getByLabel('ZIP code').fill('75201');

  await page.getByTestId('place-order-button').click();
  await expect(page).toHaveURL(/\/checkout\/fake-pay\?session_id=cs_fake_[a-z0-9]{26}$/);
  await page.getByTestId('fake-pay-button').click();
  await expect(page).toHaveURL(/\/orders\/[a-z0-9]{26}\?access=[^&]+&placed=1$/);
}

async function adminLogin(page: Page) {
  await page.goto('/admin/login');
  await page.getByLabel('Password').fill(adminPassword);
  await page.getByRole('button', { name: 'Sign in' }).click();
  await expect(page).toHaveURL(/\/admin$/);
  await expect(page.getByTestId('admin-nav')).toBeVisible();
}

test('admin analytics surfaces revenue, charts, and best sellers for a paid order', async ({ page }, testInfo) => {
  await placeGuestOrder(page, uniqueEmail(testInfo, 'analytics'));

  await adminLogin(page);

  // The shared admin header exposes Analytics on every page; navigate from the
  // dashboard via the nav link.
  await page.getByTestId('admin-nav-analytics').click();
  await expect(page).toHaveURL(/\/admin\/analytics$/);
  await expect(page.getByTestId('admin-nav-analytics')).toHaveAttribute('aria-current', 'page');

  // Default reporting window is the last 30 days.
  await expect(page.getByTestId('admin-analytics-range-30d')).toHaveAttribute('aria-current', 'page');

  // KPI tiles render with money values.
  await expect(page.getByTestId('admin-analytics-gross-revenue')).toContainText('$');
  await expect(page.getByTestId('admin-analytics-net-revenue')).toContainText('$');
  await expect(page.getByTestId('admin-analytics-aov')).toContainText('$');
  await expect(page.getByTestId('admin-analytics-orders')).toBeVisible();

  // The server-rendered SVG time series is present (CSP-safe, no client JS).
  await expect(page.locator('#admin-main svg')).toBeVisible();

  // The order's product shows up in best sellers.
  await expect(
    page.getByTestId('admin-analytics-product-row').filter({ hasText: productName }).first(),
  ).toBeVisible();

  // Switching the reporting period updates the URL and the active preset.
  await page.getByTestId('admin-analytics-range-7d').click();
  await expect(page).toHaveURL(/\/admin\/analytics\?range=7d$/);
  await expect(page.getByTestId('admin-analytics-range-7d')).toHaveAttribute('aria-current', 'page');
  await expect(page.getByTestId('admin-analytics-range-30d')).not.toHaveAttribute('aria-current', 'page');

  // The product remains visible inside the 7-day window too.
  await expect(
    page.getByTestId('admin-analytics-product-row').filter({ hasText: productName }).first(),
  ).toBeVisible();
});
