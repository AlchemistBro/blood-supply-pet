import unittest
from unittest.mock import patch
import application


class RestoreApplicationTests(unittest.TestCase):
    def test_database_endpoint_only_changes_host(self):
        raw = 'postgres://matching:p%40ss@pg-rw.bs-data.svc:5432/matching?sslmode=require'
        self.assertEqual(application.database_url(raw), raw.replace('@pg-rw.', '@pg-drill-rw.'))
        for bad in [raw.replace('pg-rw.bs-data.svc', 'external.example'),
                    raw.replace(':5432/', ':9999/'), raw.replace('postgres://', 'https://')]:
            with self.subTest(url=bad), self.assertRaises(ValueError):
                application.database_url(bad)

    def test_real_keycloak_form_preserves_hidden_fields_and_unescapes_action(self):
        form = application.LoginForm()
        form.feed('<form id="other"><input name="outside" value="bad"></form>'
                  '<form id="kc-form-login" action="/auth/login?state=x&amp;tab=y">'
                  '<input type="hidden" name="credentialId" value="nonce">'
                  '<input name="username"><input name="password" type="password"></form>'
                  '<input name="outside" value="bad">')
        self.assertEqual(form.action, '/auth/login?state=x&tab=y')
        self.assertEqual(form.fields, {'credentialId': 'nonce', 'username': '', 'password': ''})

    def test_restore_access_is_scoped_to_database_clients_and_one_port(self):
        policies = application.restore_policies()
        self.assertEqual(len(policies), 3)
        for p in policies:
            spec = p['spec']
            direction = spec['policyTypes'][0].lower()
            self.assertEqual(spec[direction][0]['ports'], [{'protocol': 'TCP', 'port': 5432}])
        ingress = policies[0]['spec']['ingress'][0]['from']
        self.assertEqual(ingress[0]['podSelector']['matchExpressions'][0]['values'], application.DB_SERVICES)
        self.assertNotIn('web', application.DB_SERVICES)
        self.assertEqual(policies[1]['spec']['egress'][0]['to'][0]['podSelector'],
                         {'matchLabels': {'cnpg.io/cluster': 'pg-drill'}})

    def test_database_oracle_rejects_sql_injection_before_any_kubectl(self):
        with patch.object(application, 'kubectl') as command:
            with self.assertRaises(ValueError):
                application.count('pg-drill', "'; DROP TABLE clinical_request; --")
            command.assert_not_called()


if __name__ == '__main__':
    unittest.main()
