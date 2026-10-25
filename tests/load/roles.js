import http from 'k6/http';
import exec from 'k6/execution';
import { check, sleep } from 'k6';
import { Counter } from 'k6/metrics';

// Только синтетический стенд; реальный запуск требует явной переменной и не выполняется при сборке.
if (__ENV.BS_LOAD_SYNTHETIC_ONLY !== '1') {
  throw new Error('Для демо-стенда задайте BS_LOAD_SYNTHETIC_ONLY=1');
}
const base = (__ENV.BS_LOAD_WEB || 'https://localhost').replace(/\/$/, '');
const password = __ENV.BS_LOAD_PASSWORD || 'demo';
let signedIn = false;
let reportedLoginFailure = false;
const rampUpSeconds = 120;
const steadySeconds = 120;
const steadyUsers = new Counter('steady_users');
let countedSteadyUser = false;

function scenario(name, users) {
  return {
    executor: 'ramping-vus', exec: name, startVUs: 0,
    stages: [{ duration: `${rampUpSeconds}s`, target: users }, { duration: `${steadySeconds}s`, target: users }, { duration: '30s', target: 0 }],
    gracefulRampDown: '15s',
  };
}
export const options = {
  insecureSkipTLSVerify: __ENV.BS_LOAD_INSECURE_TLS === '1',
  // Состояние VU сохраняется между итерациями; его cookie сессии должны сохраняться тоже.
  noCookiesReset: true,
  scenarios: { operator: scenario('operator', 20), physician: scenario('physician', 20), analyst: scenario('analyst', 10) },
  thresholds: {
    'http_req_duration{scope:screen,phase:steady}': ['p(95)<2000'],
    'http_req_failed{scope:screen,phase:steady}': ['rate<0.01'],
    'http_reqs{scope:screen,phase:steady}': ['count>100'],
    steady_users: ['count>=50'],
    checks: ['rate>0.99'],
  },
};
function params(name, scope = 'screen') {
  const elapsed = exec.instance.currentTestRunDuration;
  return { redirects: 10, timeout: '15s', tags: { name, scope, phase: elapsed >= rampUpSeconds * 1000 && elapsed < (rampUpSeconds + steadySeconds) * 1000 ? 'steady' : 'ramp' } };
}
function login(user, next) {
  if (signedIn) return true;
  let response = http.get(`${base}/auth/login?hint=${encodeURIComponent(user)}&next=${encodeURIComponent(next)}`, params('OIDC start', 'auth'));
  if (response.html().find('#kc-form-login').size() > 0) {
    response = response.submitForm({ formSelector: '#kc-form-login', fields: { username: user, password }, params: params('OIDC credentials', 'auth') });
  }
  signedIn = check(response, { 'сессия создана': (r) => r.status === 200 && r.url.startsWith(base) && !r.url.includes('/login') });
  if (!signedIn && !reportedLoginFailure) {
    reportedLoginFailure = true;
    // Только статус и путь: параметры callback, токены и тело ответа в журнал не попадают.
    console.warn(`Вход ${user}: HTTP ${response.status}, ${response.url.split('?')[0]}`);
  }
  // Не превращаем ошибку входа в непрерывный поток повторов к лимитированному /auth/.
  if (!signedIn) sleep(10 + Math.random() * 10);
  return signedIn;
}
function page(path, name) {
  const response = http.get(base + path, params(name));
  const available = check(response, { 'экран доступен': (r) => r.status === 200 && !r.url.includes('/login') });
  if (!available) signedIn = false;
  const elapsed = exec.instance.currentTestRunDuration;
  if (available && !countedSteadyUser && elapsed >= rampUpSeconds * 1000 && elapsed < (rampUpSeconds + steadySeconds) * 1000) {
    countedSteadyUser = true;
    steadyUsers.add(1);
  }
  return response;
}
export function operator() {
  if (!login('operator-c001', '/stock')) return;
  page('/stock', 'Запас клиники');
  page('/tasks', 'Задания оператора');
  sleep(1);
}
export function physician() {
  if (!login('physician1', '/requests')) return;
  if (__ITER % 5 === 0) {
    const form = page('/requests/new', 'Новая заявка');
    if (form.html().find('form[action="/requests"]').size() > 0) {
      const response = form.submitForm({
        formSelector: 'form[action="/requests"]',
        fields: { patient_case_no: `LOAD-${__VU}-${__ITER}`, component_kind: 'RBC', requested_units: '1', urgency: 'PLANNED', blood_group: 'O+', phenotype: '' },
        params: params('Создание заявки и варианты'),
      });
      check(response, { 'варианты или дефицит показаны': (r) => r.status === 200 && /\/requests\/[0-9a-f-]{36}/.test(r.url) });
    }
  } else {
    page('/requests', 'Заявки врача');
  }
  sleep(1);
}
export function analyst() {
  if (!login('analyst1', '/demand')) return;
  const response = page('/demand', 'Версии потребности');
  const detail = response.html().find('a[href^="/demand/estimates/"]').first().attr('href');
  if (detail) page(detail, 'Версия оценки');
  sleep(1);
}
export function handleSummary(data) {
  return { 'tests/load/results/summary.json': JSON.stringify(data, null, 2), stdout: 'Нагрузка завершена; фактические показатели: tests/load/results/summary.json\n' };
}
