import { expect, test, type APIRequestContext, type Page, type TestInfo } from '@playwright/test';

import { clearCapturedEmails, listCapturedEmails, type CapturedEmailMessage } from './email-capture';

const adminPassword = 'admin-product-password';
const productSlug = 'handwoven-indigo-scarf';
const productName = 'Handwoven Indigo Scarf';
const scarfVariant = 'M';
const trackingCarrier = 'Thailand Post';
const trackingNumber = 'TGS-TRACK-123';

type EmailCriteria = {
  to?: string;
  kind?: string;
  eventKey?: string | RegExp;
};

function uniqueGuestOrderEmail(testInfo: TestInfo, label: string): string {
  return `guest-order-email-${label}-${testInfo.workerIndex}-${Date.now()}@example.test`;
}

async function addScarfToCart(page: Page) {
  await page.goto(`/products/${productSlug}`);
  await page.getByTestId('variant-select').selectOption({ label: scarfVariant });
  await page.getByRole('button', { name: 'Add to cart' }).click();
  await expect(page).toHaveURL(/\/cart$/);
}

async function fillGuestCheckoutForm(page: Page, email: string, fullName: string) {
  const form = page.getByTestId('guest-checkout-form');
  await expect(form).toBeVisible();
  await page.getByTestId('guest-email-input').fill(email);
  await form.getByLabel('Full name').fill(fullName);
  await form.getByLabel('Address line 1').fill('88 Charoen Krung Road');
  await form.getByLabel('City').fill('Seattle');
  await form.getByLabel('State').fill('WA');
  await form.getByLabel('ZIP code').fill('98101');
}

async function placePaidGuestScarfOrder(page: Page, email: string, fullName: string): Promise<string> {
  await addScarfToCart(page);
  await page.getByRole('link', { name: 'Check out', exact: true }).click();
  await expect(page).toHaveURL(/\/checkout$/);
  await fillGuestCheckoutForm(page, email, fullName);
  await page.getByTestId('place-order-button').click();
  await expect(page).toHaveURL(/\/checkout\/fake-pay\?session_id=cs_fake_[a-z0-9]{26}$/);
  await page.getByTestId('fake-pay-button').click();
  await expect(page.getByTestId('order-placed-banner')).toBeVisible();
  const orderURL = new URL(page.url());
  expect(orderURL.pathname).toMatch(/^\/orders\/[a-z0-9]{26}$/);
  expect(orderURL.searchParams.has('access')).toBe(true);
  expect(orderURL.searchParams.get('placed')).toBe('1');
  const orderID = orderURL.pathname.split('/').pop() as string;
  await expect(page.getByTestId('order-status')).toHaveText('Paid');
  await expect(page.getByTestId('order-line-item').filter({ hasText: productName })).toBeVisible();
  return orderID;
}

async function adminLogin(page: Page) {
  await page.goto('/admin/login');
  await page.getByLabel('Password').fill(adminPassword);
  await page.getByRole('button', { name: 'Sign in' }).click();
  await expect(page).toHaveURL(/\/admin$/);
  await expect(page.getByTestId('admin-nav')).toBeVisible();
}

async function openAdminOrder(page: Page, orderID: string) {
  await page.goto('/admin/orders');
  const row = page.getByTestId('admin-order-row').filter({ hasText: orderID });
  await expect(row).toBeVisible();
  await row.getByRole('link', { name: 'Review' }).click();
  await expect(page).toHaveURL(new RegExp(`/admin/orders/${orderID}$`));
}

async function messagesMatching(request: APIRequestContext, criteria: EmailCriteria): Promise<CapturedEmailMessage[]> {
  return (await listCapturedEmails(request)).filter((message) => matchesEmail(message, criteria));
}

async function waitForMessageCount(request: APIRequestContext, criteria: EmailCriteria, count: number): Promise<CapturedEmailMessage[]> {
  await expect
    .poll(async () => (await messagesMatching(request, criteria)).length, { timeout: 5_000 })
    .toBe(count);
  return messagesMatching(request, criteria);
}

function matchesEmail(message: CapturedEmailMessage, criteria: EmailCriteria): boolean {
  if (criteria.to !== undefined && message.to !== criteria.to) {
    return false;
  }
  if (criteria.kind !== undefined && message.kind !== criteria.kind) {
    return false;
  }
  if (criteria.eventKey instanceof RegExp) {
    return criteria.eventKey.test(message.eventKey);
  }
  if (criteria.eventKey !== undefined && message.eventKey !== criteria.eventKey) {
    return false;
  }
  return true;
}

function eventKeyPattern(orderID: string, suffix: string): RegExp {
  return new RegExp(`^order:${orderID}:${suffix}:v\\d+$`);
}

function assertGuestEmailUsesConfirmLink(message: CapturedEmailMessage, orderID: string) {
  const content = `${message.text}\n${message.html}`;
  expect(content.includes('/checkout/confirm?session_id=')).toBe(true);
  expect(content.includes(`/orders/${orderID}`)).toBe(false);
}

test.describe('order transactional emails', () => {
  test.describe.configure({ mode: 'serial' });

  test.use({
    javaScriptEnabled: false,
    viewport: { width: 1280, height: 2200 },
  });

  test('guest checkout and admin updates send each order email once', async ({ page, request }, testInfo) => {
    const trackingEmail = uniqueGuestOrderEmail(testInfo, 'tracking');
    const directStatusEmail = uniqueGuestOrderEmail(testInfo, 'direct-status');

    await clearCapturedEmails(request, { to: trackingEmail });
    await clearCapturedEmails(request, { to: directStatusEmail });

    const trackingOrderID = await placePaidGuestScarfOrder(page, trackingEmail, 'Guest Order Email Tracking');
    const [placedMessage] = await waitForMessageCount(request, { to: trackingEmail, kind: 'order_placed' }, 1);
    await waitForMessageCount(request, { to: trackingEmail }, 1);
    expect(placedMessage.to).toBe(trackingEmail);
    expect(placedMessage.subject).toBe(`Order ${trackingOrderID} was placed`);
    expect(placedMessage.eventKey).toMatch(eventKeyPattern(trackingOrderID, 'placed'));
    expect(placedMessage.text.includes(trackingOrderID) || placedMessage.html.includes(trackingOrderID)).toBe(true);
    expect(placedMessage.text.includes('Status: paid') || placedMessage.html.includes('Status: paid')).toBe(true);
    assertGuestEmailUsesConfirmLink(placedMessage, trackingOrderID);

    await adminLogin(page);
    await openAdminOrder(page, trackingOrderID);
    await expect(page.getByTestId('admin-order-status')).toHaveText('Paid');
    await page.getByTestId('admin-order-carrier-input').fill(trackingCarrier);
    await page.getByTestId('admin-order-tracking-input').fill(trackingNumber);
    await page.getByTestId('admin-order-tracking-button').click();
    await expect(page).toHaveURL(new RegExp(`/admin/orders/${trackingOrderID}\\?saved=shipped$`));
    await expect(page.getByTestId('admin-order-status')).toHaveText('Shipped');
    await expect(page.getByTestId('admin-order-tracking-value')).toHaveText(`${trackingCarrier} ${trackingNumber}`);

    const [trackingMessage] = await waitForMessageCount(request, { to: trackingEmail, kind: 'tracking_update' }, 1);
    expect(trackingMessage.subject).toBe(`Tracking update for order ${trackingOrderID}`);
    expect(trackingMessage.eventKey).toMatch(eventKeyPattern(trackingOrderID, 'tracking'));
    expect(trackingMessage.text.includes(trackingNumber) || trackingMessage.html.includes(trackingNumber)).toBe(true);
    assertGuestEmailUsesConfirmLink(trackingMessage, trackingOrderID);

    const trackingEventKey = trackingMessage.eventKey;
    await page.getByTestId('admin-order-carrier-input').fill(trackingCarrier);
    await page.getByTestId('admin-order-tracking-input').fill(trackingNumber);
    await page.getByTestId('admin-order-tracking-button').click();
    await expect(page).toHaveURL(new RegExp(`/admin/orders/${trackingOrderID}\\?saved=shipped$`));
    await waitForMessageCount(request, { to: trackingEmail, kind: 'tracking_update' }, 1);
    const trackingMessagesAfterRetry = await messagesMatching(request, { to: trackingEmail, kind: 'tracking_update' });
    expect(trackingMessagesAfterRetry.map((message) => message.eventKey)).toEqual([trackingEventKey]);

    await page.getByTestId('admin-order-advance-status').selectOption('delivered');
    await page.getByTestId('admin-order-advance-button').click();
    await expect(page).toHaveURL(new RegExp(`/admin/orders/${trackingOrderID}\\?saved=advanced$`));
    await expect(page.getByTestId('admin-order-status')).toHaveText('Delivered');
    const [deliveredMessage] = await waitForMessageCount(request, {
      to: trackingEmail,
      kind: 'order_status_change',
      eventKey: eventKeyPattern(trackingOrderID, 'status:shipped:delivered'),
    }, 1);
    expect(deliveredMessage.subject).toBe(`Order ${trackingOrderID} status: delivered`);
    assertGuestEmailUsesConfirmLink(deliveredMessage, trackingOrderID);

    const directStatusOrderID = await placePaidGuestScarfOrder(page, directStatusEmail, 'Guest Order Email Direct Status');
    const [directPlacedMessage] = await waitForMessageCount(request, { to: directStatusEmail, kind: 'order_placed' }, 1);
    assertGuestEmailUsesConfirmLink(directPlacedMessage, directStatusOrderID);
    await openAdminOrder(page, directStatusOrderID);
    await expect(page.getByTestId('admin-order-status')).toHaveText('Paid');
    await page.getByTestId('admin-order-advance-status').selectOption('shipped');
    await page.getByTestId('admin-order-advance-button').click();
    await expect(page).toHaveURL(new RegExp(`/admin/orders/${directStatusOrderID}\\?saved=advanced$`));
    await expect(page.getByTestId('admin-order-status')).toHaveText('Shipped');
    const [shippedMessage] = await waitForMessageCount(request, {
      to: directStatusEmail,
      kind: 'order_status_change',
      eventKey: eventKeyPattern(directStatusOrderID, 'status:paid:shipped'),
    }, 1);
    expect(shippedMessage.subject).toBe(`Order ${directStatusOrderID} status: shipped`);
    assertGuestEmailUsesConfirmLink(shippedMessage, directStatusOrderID);

    await waitForMessageCount(request, { to: trackingEmail }, 3);
    await waitForMessageCount(request, { to: directStatusEmail }, 2);
  });
});
