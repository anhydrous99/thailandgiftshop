import { expect, test, type Page, type TestInfo } from '@playwright/test';

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

test.describe('without JavaScript', () => {
  test.use({
    javaScriptEnabled: false,
    // The site shell sets scroll-smooth on <html>; with page JavaScript
    // disabled, Playwright's scroll-into-view animation for below-the-fold
    // controls never settles, so use a viewport tall enough to avoid
    // scrolling altogether.
    viewport: { width: 1280, height: 2400 },
  });

  test('setup flow saves the demo card, lists it, and removes it', async ({ page }, testInfo) => {
    const email = uniqueEmail(testInfo, 'cards');
    await signUp(page, email);

    await page.goto('/account/payment-methods');
    await expect(page.getByRole('heading', { name: 'Saved cards', level: 1 })).toBeVisible();
    await expect(page.getByText('No saved cards yet.')).toBeVisible();
    await expect(page.getByTestId('payment-method-row')).toHaveCount(0);

    // Adding a card runs the provider setup session; in demo mode that is the
    // on-site fake-pay page in setup mode.
    await page.getByRole('button', { name: 'Add a card' }).click();
    await expect(page).toHaveURL(/\/checkout\/fake-pay\?session_id=cs_fake_setup_[a-z0-9]{26}$/);
    await expect(page.getByRole('heading', { name: 'Save a demo card', level: 1 })).toBeVisible();
    await expect(page.getByText('Demo payment — no real charge')).toBeVisible();

    // Completing setup returns to the list with the saved banner and the card.
    await page.getByTestId('fake-pay-button').click();
    await expect(page).toHaveURL(/\/account\/payment-methods\?saved=1$/);
    await expect(page.getByText('Card saved.')).toBeVisible();
    const cardRow = page.getByTestId('payment-method-row').filter({ hasText: '4242' });
    await expect(cardRow).toBeVisible();
    await expect(cardRow.getByText(/Visa •••• 4242/)).toBeVisible();
    await expect(cardRow.getByText(/Expires 12\/\d{4}/)).toBeVisible();

    // Removing the card returns to the empty state.
    await cardRow.getByRole('button', { name: 'Remove' }).click();
    await expect(page).toHaveURL(/\/account\/payment-methods$/);
    await expect(page.getByTestId('payment-method-row')).toHaveCount(0);
    await expect(page.getByText('No saved cards yet.')).toBeVisible();
  });

  test('canceling the setup flow leaves no card saved', async ({ page }, testInfo) => {
    const email = uniqueEmail(testInfo, 'cards-cancel');
    await signUp(page, email);

    await page.goto('/account/payment-methods');
    await page.getByRole('button', { name: 'Add a card' }).click();
    await expect(page).toHaveURL(/\/checkout\/fake-pay\?session_id=cs_fake_setup_[a-z0-9]{26}$/);

    await page.getByTestId('fake-pay-cancel').click();
    await expect(page).toHaveURL(/\/account\/payment-methods$/);
    await expect(page.getByTestId('payment-method-row')).toHaveCount(0);
    await expect(page.getByText('No saved cards yet.')).toBeVisible();
  });
});
