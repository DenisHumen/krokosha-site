// The contact form in a real browser (brief B10.1). What the API does with a request is tested in
// Go (api/internal/leads); here — what a visitor sees and what exactly the browser sends.
import { createHash } from 'node:crypto';
import type { Page, Route } from '@playwright/test';
import { expect, LOCALES, test } from './fixtures.ts';

// A puzzle with a known answer: SHA-256(salt + 1234).
const SALT = 'a1b2c3d4e5f60718293a4b5c?expires=4102444800&';
const CHALLENGE = {
  algorithm: 'SHA-256',
  challenge: createHash('sha256').update(`${SALT}1234`).digest('hex'),
  maxnumber: 5000,
  salt: SALT,
  signature: 'signed-by-the-server',
};

interface Sent {
  fields: Record<string, string>;
  accept: string;
}

/** Answers for the API; returns what the form posted. */
async function mockApi(page: Page, respond: (route: Route) => Promise<void>): Promise<Sent[]> {
  const sent: Sent[] = [];
  await page.route('**/api/e', (route) => route.fulfill({ status: 204 }));
  await page.route('**/api/leads/challenge', (route) => route.fulfill({ json: CHALLENGE }));
  await page.route('**/api/leads', async (route) => {
    const request = route.request();
    const body = request.postData() ?? '';
    const fields: Record<string, string> = {};
    // multipart/form-data: «name="x"\r\n\r\nvalue\r\n--boundary»
    for (const match of body.matchAll(/name="([^"]+)"\r\n\r\n([\s\S]*?)\r\n--/g)) {
      fields[match[1] ?? ''] = match[2] ?? '';
    }
    sent.push({ fields, accept: request.headers()['accept'] ?? '' });
    await respond(route);
  });
  return sent;
}

async function fill(page: Page) {
  await page.getByLabel('Name', { exact: true }).fill('Ivan Petrov');
  await page.locator('#form-contact').fill('ivan@company.com');
  await page.getByLabel('Area').selectOption('networks');
  await page
    .locator('#form-description')
    .fill('Office network for forty seats: MikroTik and two VLANs.');
  await page.locator('#form-budget').selectOption({ index: 3 });
  await page.getByRole('checkbox').check();
}

/** Tests that make the API fail on purpose take those failures off the list of surprises. */
function expectProblems(problems: string[], pattern: RegExp) {
  const expected = problems.filter((problem) => pattern.test(problem));
  expect(expected.length, `expected a problem like ${pattern}`).toBeGreaterThan(0);
  for (const problem of expected) problems.splice(problems.indexOf(problem), 1);
}

test.describe('contact form', () => {
  for (const { code, home } of LOCALES) {
    test(`is there, in the language of the page: ${code}`, async ({ page }) => {
      await mockApi(page, (route) => route.fulfill({ status: 500 }));
      await page.goto(home);
      const form = page.locator('form[data-form]');
      await expect(form).toBeVisible();
      await expect(form.locator('input[name="lang"]')).toHaveValue(code);
      // Every field has a label a screen reader announces.
      for (const field of ['#form-name', '#form-contact', '#form-direction', '#form-description']) {
        const input = page.locator(field);
        await expect(input).toBeVisible();
        expect(
          await input.evaluate((node: HTMLInputElement) => node.labels?.length ?? 0),
        ).toBeGreaterThan(0);
      }
      // The trap for robots is out of people's way.
      const trap = form.locator('input[name="website"]');
      await expect(trap).toHaveAttribute('tabindex', '-1');
      expect(await trap.evaluate((node) => node.getBoundingClientRect().right)).toBeLessThan(0);
    });
  }

  test('sends a request and says thank you', async ({ page }) => {
    const sent = await mockApi(page, (route) =>
      route.fulfill({
        status: 201,
        json: { ok: true, id: 'K-0042', telegram_url: 'https://t.me/krokosha_bot?start=c_AbCdEf' },
      }),
    );
    await page.goto('/');
    await fill(page);
    await page.getByRole('button', { name: 'Send request' }).click();

    const success = page.locator('#form-success');
    await expect(success).toBeVisible();
    await expect(success.getByRole('heading')).toHaveText('Request #K-0042 received');
    await expect(success.getByRole('link', { name: 'Continue in Telegram' })).toHaveAttribute(
      'href',
      'https://t.me/krokosha_bot?start=c_AbCdEf',
    );
    await expect(page.locator('form[data-form]')).toBeHidden();

    expect(sent).toHaveLength(1);
    const { fields, accept } = sent[0]!;
    expect(accept).toContain('application/json');
    expect(fields).toMatchObject({
      name: 'Ivan Petrov',
      contact_method: 'email',
      contact_value: 'ivan@company.com',
      direction: 'networks',
      budget: '2',
      timeline: '',
      consent: 'on',
      lang: 'en',
      website: '',
    });
    // The puzzle was solved in the browser while the form was being filled in.
    const proof = JSON.parse(Buffer.from(fields['altcha'] ?? '', 'base64').toString()) as Record<
      string,
      unknown
    >;
    expect(proof).toMatchObject({
      algorithm: 'SHA-256',
      number: 1234,
      salt: SALT,
      signature: 'signed-by-the-server',
    });
  });

  test('checks the fields before sending, in the language of the page', async ({ page }) => {
    const sent = await mockApi(page, (route) => route.fulfill({ status: 500 }));
    await page.goto('/ru/');
    await page.getByRole('button', { name: 'Отправить заявку' }).click();

    await expect(page.locator('[data-error-for="name"]')).toHaveText('Обязательное поле');
    await expect(page.locator('[data-error-for="consent"]')).toHaveText(
      'Без согласия заявку не отправить',
    );
    await expect(page.locator('#form-name')).toHaveAttribute('aria-invalid', 'true');
    await expect(page.locator('#form-name')).toBeFocused();

    await page.locator('#form-name').fill('Иван');
    await expect(page.locator('[data-error-for="name"]')).toBeEmpty();
    await page.locator('#form-contact').fill('ivan@');
    await page.locator('#form-description').fill('коротко');
    await page.getByRole('button', { name: 'Отправить заявку' }).click();
    await expect(page.locator('[data-error-for="contact_value"]')).toHaveText(
      'Проверьте адрес почты',
    );
    await expect(page.locator('[data-error-for="description"]')).toHaveText(
      'От 20 до 4000 символов',
    );

    // The contact field follows the chosen method.
    await page.getByRole('radio', { name: 'Telegram' }).check();
    await expect(page.locator('#form-contact')).toHaveAttribute('placeholder', '@username');
    await page.getByRole('button', { name: 'Отправить заявку' }).click();
    await expect(page.locator('[data-error-for="contact_value"]')).toHaveText(
      'Проверьте ник в Telegram',
    );
    await page.getByRole('radio', { name: 'Телефон' }).check();
    await expect(page.locator('#form-contact')).toHaveAttribute('type', 'tel');
    await page.locator('#form-contact').fill('+380 (67) 123-45-67');
    await page.getByRole('button', { name: 'Отправить заявку' }).click();
    await expect(page.locator('[data-error-for="contact_value"]')).toBeEmpty();

    expect(sent, 'nothing is sent while the form has mistakes').toHaveLength(0);
  });

  test('shows what the server did not like', async ({ page, problems }) => {
    await mockApi(page, (route) =>
      route.fulfill({
        status: 422,
        json: { ok: false, error: 'invalid', errors: { contact_value: 'invalid_email' } },
      }),
    );
    await page.goto('/uk/');
    await page.locator('#form-name').fill('Іван');
    await page.locator('#form-contact').fill('ivan@company.com');
    await page.locator('#form-direction').selectOption('devops');
    await page
      .locator('#form-description')
      .fill('Потрібно налаштувати CI/CD для невеликого проєкту.');
    await page.getByRole('checkbox').check();
    await page.getByRole('button', { name: 'Надіслати заявку' }).click();

    await expect(page.locator('[data-error-for="contact_value"]')).toHaveText(
      'Перевірте адресу пошти',
    );
    await expect(page.locator('#form-error-invalid')).toBeVisible();
    await expect(page.getByRole('button', { name: 'Надіслати заявку' })).toBeEnabled();
    expectProblems(problems, /422/);
  });

  for (const { status, block, text } of [
    { status: 429, block: '#form-error-rate', text: 'Too many requests' },
    {
      status: 500,
      block: '#form-error-server',
      text: "Couldn't send the request. Please email me: denis@krokosha.com",
    },
  ]) {
    test(`explains a failure of the server: ${status}`, async ({ page, problems }) => {
      await mockApi(page, (route) => route.fulfill({ status, json: { ok: false } }));
      await page.goto('/');
      await fill(page);
      await page.getByRole('button', { name: 'Send request' }).click();
      await expect(page.locator(block)).toBeVisible();
      await expect(page.locator(block)).toContainText(text);
      await expect(page.locator('form[data-form]')).toBeVisible(); // what was typed is not lost
      await expect(page.locator('#form-name')).toHaveValue('Ivan Petrov');
      expectProblems(problems, new RegExp(String(status)));
    });
  }
});

test.describe('contact form without JavaScript', () => {
  test.use({ javaScriptEnabled: false });

  test('is a plain form that posts to the API', async ({ page }) => {
    await page.goto('/');
    const form = page.locator('form[data-form]');
    await expect(form).toHaveAttribute('method', 'post');
    await expect(form).toHaveAttribute('action', '/api/leads');
    for (const field of ['#form-name', '#form-contact', '#form-direction', '#form-description']) {
      await expect(page.locator(field)).toHaveAttribute('required', '');
    }
    await expect(page.locator('#form-description')).toHaveAttribute('minlength', '20');
    // Files are off unless content/site.yaml says «attachments: true» (contract §7, v1.4).
    await expect(form.locator('input[type="file"]')).toHaveCount(0);
    await expect(form).not.toHaveAttribute('enctype', 'multipart/form-data');
    await expect(page.locator('#form-success')).toBeHidden();
    await expect(page.locator('#form-error-rate')).toBeHidden();
  });

  test('shows the message the API redirects to', async ({ page }) => {
    await page.goto('/ru/#form-error-rate');
    await expect(page.locator('#form-error-rate')).toBeVisible();
    await expect(page.locator('#form-error-rate')).toContainText('Слишком много заявок');
    await expect(page.locator('#form-error-server')).toBeHidden();
  });

  for (const { code, home } of LOCALES) {
    test(`thank-you page is presentable as built: ${code}`, async ({ page }) => {
      await page.goto(`${home}thanks/`);
      await expect(page.locator('html')).toHaveAttribute('lang', code);
      await expect(page.locator('[data-field="generic"]')).toBeVisible();
      await expect(page.locator('[data-field="numbered"]')).toBeHidden();
      await expect(page.locator('[data-field="telegram"]')).toBeHidden();
      expect(await page.locator('main, body').first().innerText()).not.toContain('%%');
      await expect(page.locator('meta[name="robots"]')).toHaveAttribute('content', /noindex/);
    });
  }
});
