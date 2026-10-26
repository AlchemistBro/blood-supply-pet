import copy
import unittest
import evidence


class RuntimeEvidenceTests(unittest.TestCase):
    def fixture(self):
        return {
            'manifest': {'source_commit': 'a' * 40, 'candidate_run_id': '123', 'version': 'v1.0.0'},
            'startup': {'mode': 'release', 'exit_code': 0, 'elapsed_seconds': 599},
            'chaos': {'checks': [{'name': name, 'status': 'passed', 'evidence': {
                'mailpit_delivery': {'message_id': 'message-1'}}} for name in ('gateway', 'kafka', 'keycloak', 'dlt')],
                'cleanup_errors': []},
            'application': {**{name: True for name in ('pre_backup_request_restored', 'encrypted_case_decrypted',
                'new_request_in_restored_only', 'business_saga_after_restore', 'original_endpoints_restored')},
                'browser': [{'user': user, 'path': path, 'status': 200} for user, path in
                            [('analyst1', '/demand'), ('physician1', '/requests'), ('operator-c001', '/stock')]]},
            'readiness': {'items': [{'spec': {'replicas': 1}, 'status': {'availableReplicas': 1}} for _ in range(9)]},
            'hpa': {'items': [{'metadata': {'name': name}, 'status': {'conditions': [
                {'type': 'ScalingActive', 'status': 'True'}]}} for name in ('web', 'inventory', 'matching')]},
            'images_run_id': '123', 'commit': 'a' * 40, 'version': 'v1.0.0', 'digest': 'b' * 64}

    def test_evidence_requires_all_runtime_gates(self):
        valid = self.fixture()
        self.assertEqual(evidence.build(**valid)['startup_seconds'], 599)
        for change in ('slow', 'wrong_images', 'cleanup_failed', 'missing_delivery', 'saga_failed', 'role_failed', 'hpa_failed'):
            row = copy.deepcopy(valid)
            if change == 'slow': row['startup']['elapsed_seconds'] = 601
            if change == 'wrong_images': row['manifest']['candidate_run_id'] = '124'
            if change == 'cleanup_failed': row['chaos']['cleanup_errors'] = ['gateway']
            if change == 'missing_delivery': row['chaos']['checks'][3]['evidence'] = {}
            if change == 'saga_failed': row['application']['business_saga_after_restore'] = False
            if change == 'role_failed': row['application']['browser'][0]['status'] = 503
            if change == 'hpa_failed': row['hpa']['items'][0]['status']['conditions'][0]['status'] = 'False'
            with self.subTest(change=change), self.assertRaises(ValueError):
                evidence.build(**row)


if __name__ == '__main__':
    unittest.main()
