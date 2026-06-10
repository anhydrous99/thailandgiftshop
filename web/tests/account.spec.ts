import { expect, test, type Page, type TestInfo } from '@playwright/test';

const password = 'playwright-demo-password';

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
  await expect(page.getByRole('heading', { name: 'Your account', level: 1 })).toBeVisible();
}

type AddressFormData = {
  fullName: string;
  line1: string;
  city: string;
  region: string;
  postalCode: string;
};

async function fillAddressForm(page: Page, data: AddressFormData) {
  const form = page.getByTestId('address-form');
  await form.getByLabel('Full name').fill(data.fullName);
  await form.getByLabel('Address line 1').fill(data.line1);
  await form.getByLabel('City').fill(data.city);
  await form.getByLabel('State').fill(data.region);
  await form.getByLabel('ZIP code').fill(data.postalCode);
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

  test('sign-up signs the shopper in, sign-out clears the session, sign-in rejects a wrong password generically', async ({ page }, testInfo) => {
    const email = uniqueEmail(testInfo, 'shopper');

    await signUp(page, email);
    await expect(page.getByText(`Signed in as ${email}`)).toBeVisible();

    // A signed-in shopper visiting the sign-in page is sent back to the account overview.
    await page.goto('/account/sign-in');
    await expect(page).toHaveURL(/\/account$/);

    await page.getByRole('button', { name: 'Sign out' }).click();
    await expect(page).toHaveURL(/\/$/);

    // A duplicate sign-up for the same email re-renders with the documented message.
    await page.goto('/account/sign-up');
    await page.getByLabel('Email').fill(email);
    await page.getByLabel('Password (8 to 72 characters)').fill(password);
    await page.getByRole('button', { name: 'Create account' }).click();
    await expect(page.getByRole('alert')).toContainText('An account with this email already exists. Sign in instead.');

    // Protected pages now bounce to sign-in with a validated return_to.
    await page.goto('/account');
    await expect(page).toHaveURL(/\/account\/sign-in\?return_to=%2Faccount$/);
    await expect(page.getByTestId('signin-form')).toBeVisible();
    await expect(page.getByText('Forgot your password? Reset is not available yet')).toBeVisible();

    // A wrong password gets the generic error and never reveals whether the email exists.
    await page.getByLabel('Email').fill(email);
    await page.getByLabel('Password').fill('not-the-password');
    await page.getByRole('button', { name: 'Sign in' }).click();
    await expect(page.getByRole('alert')).toContainText('Invalid email or password.');
    await expect(page.getByTestId('signin-form')).toBeVisible();

    // The correct password signs in and honors the preserved return_to.
    await page.getByLabel('Email').fill(email);
    await page.getByLabel('Password').fill(password);
    await page.getByRole('button', { name: 'Sign in' }).click();
    await expect(page).toHaveURL(/\/account$/);
    await expect(page.getByText(`Signed in as ${email}`)).toBeVisible();
  });

  test('address CRUD creates edits defaults and removes saved addresses', async ({ page }, testInfo) => {
    const email = uniqueEmail(testInfo, 'addresses');
    await signUp(page, email);

    await page.goto('/account/addresses');
    await expect(page.getByRole('heading', { name: 'Addresses', level: 1 })).toBeVisible();
    await expect(page.getByText('No addresses yet.')).toBeVisible();
    await expect(page.getByTestId('address-row')).toHaveCount(0);

    // Create the first address.
    await fillAddressForm(page, {
      fullName: 'First Shopper',
      line1: '11 Charoen Krung Road',
      city: 'Austin',
      region: 'TX',
      postalCode: '78701',
    });
    await page.getByRole('button', { name: 'Save address' }).click();
    await expect(page).toHaveURL(/\/account\/addresses$/);
    const firstRow = page.getByTestId('address-row').filter({ hasText: 'First Shopper' });
    await expect(firstRow).toBeVisible();
    await expect(firstRow.getByText('11 Charoen Krung Road')).toBeVisible();

    // Create a second address.
    await fillAddressForm(page, {
      fullName: 'Second Shopper',
      line1: '22 Sukhumvit Soi',
      city: 'Dallas',
      region: 'TX',
      postalCode: '75201',
    });
    await page.getByRole('button', { name: 'Save address' }).click();
    await expect(page.getByTestId('address-row')).toHaveCount(2);

    // Make the second address the default; its Make default button disappears.
    const secondRow = page.getByTestId('address-row').filter({ hasText: 'Second Shopper' });
    await secondRow.getByRole('button', { name: 'Make default' }).click();
    await expect(page).toHaveURL(/\/account\/addresses$/);
    await expect(page.getByTestId('address-row').filter({ hasText: 'Second Shopper' }).getByText('Default')).toBeVisible();
    await expect(page.getByTestId('address-row').filter({ hasText: 'Second Shopper' }).getByRole('button', { name: 'Make default' })).toHaveCount(0);

    // The account overview shows the default address.
    await page.goto('/account');
    await expect(page.getByText('22 Sukhumvit Soi')).toBeVisible();

    // Edit the first address through its prefilled form.
    await page.goto('/account/addresses');
    await page.getByTestId('address-row').filter({ hasText: 'First Shopper' }).getByRole('link', { name: 'Edit' }).click();
    await expect(page).toHaveURL(/\/account\/addresses\/[a-z0-9]{26}\/edit$/);
    await expect(page.getByRole('heading', { name: 'Edit address', level: 1 })).toBeVisible();
    await expect(page.getByLabel('Address line 1')).toHaveValue('11 Charoen Krung Road');
    await page.getByLabel('Address line 1').fill('33 Yaowarat Road');
    await page.getByRole('button', { name: 'Save changes' }).click();
    await expect(page).toHaveURL(/\/account\/addresses$/);
    await expect(page.getByText('33 Yaowarat Road')).toBeVisible();

    // Remove the default address; the remaining one stays.
    await page.getByTestId('address-row').filter({ hasText: 'Second Shopper' }).getByRole('button', { name: 'Remove' }).click();
    await expect(page).toHaveURL(/\/account\/addresses$/);
    await expect(page.getByTestId('address-row')).toHaveCount(1);
    await expect(page.getByTestId('address-row').filter({ hasText: 'First Shopper' })).toBeVisible();

    // Removing the default clears it from the account overview.
    await page.goto('/account');
    await expect(page.getByText('No default address yet.')).toBeVisible();
  });
});

test('account overview links orders addresses and saved cards', async ({ page }, testInfo) => {
  const email = uniqueEmail(testInfo, 'overview');
  await signUp(page, email);

  await expect(page.getByText('No orders yet.')).toBeVisible();
  await expect(page.getByRole('link', { name: 'All orders' })).toHaveAttribute('href', '/orders');
  await expect(page.getByRole('link', { name: 'Manage addresses' })).toHaveAttribute('href', '/account/addresses');
  await expect(page.getByRole('link', { name: 'Saved cards' })).toHaveAttribute('href', '/account/payment-methods');
  await expect(page.getByRole('button', { name: 'Change password' })).toBeVisible();
});
