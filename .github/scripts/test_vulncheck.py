import json
import unittest

import vulncheck


class StructuredScanTest(unittest.TestCase):
    def setUp(self):
        self.expected = {"scanner_version": "v1.8.0", "go_version": "go1.27.1"}
        self.packages = {
            "roots": ["example.invalid/app"],
            "modules": {"example.invalid/app": "", "stdlib": "v1.27.1"},
        }
        self.messages = [
            {"config": dict(self.expected)},
            {"SBOM": {
                "go_version": "go1.27.1", "roots": list(self.packages["roots"]),
                "modules": [{"path": path, "version": version} for path, version in self.packages["modules"].items()],
            }},
        ]

    def evaluate(self, extra=()):
        text = "\n".join(json.dumps(value) for value in self.messages + list(extra))
        return vulncheck.evaluate(text, self.expected, self.packages)

    def test_candidate_is_not_a_finding(self):
        result = self.evaluate([{"osv": {"id": "GO-2099-0001"}}])
        self.assertEqual(result["candidate_advisories"], 1)
        self.assertFalse(result["findings_require_review"])

    def test_highest_trace_per_advisory(self):
        messages = []
        for index, frames in enumerate([
            [{"module": "example.invalid/module"}],
            [{"module": "example.invalid/module", "package": "example.invalid/module/pkg"}],
            [
                {"module": "example.invalid/module"},
                {"module": "example.invalid/module", "package": "example.invalid/module/pkg", "function": "Called"},
            ],
        ], 1):
            identifier = "GO-2099-000" + str(index)
            messages.append({"osv": {"id": identifier}})
            for frame in frames:
                messages.append({"finding": {"osv": identifier, "trace": [frame]}})
        result = self.evaluate(messages)
        self.assertEqual(result["module_only"], ["GO-2099-0001"])
        self.assertEqual(result["imported_only"], ["GO-2099-0002"])
        self.assertEqual(result["called"], ["GO-2099-0003"])
        self.assertTrue(result["findings_require_review"])

    def test_missing_or_wrong_config_fails(self):
        for text in ("", "{}", '{"progress": {}}', '{"config": {}}'):
            with self.subTest(text=text), self.assertRaises(ValueError):
                vulncheck.evaluate(text, self.expected, self.packages)

    def test_duplicate_config_or_fatal_message_fails(self):
        for message in ({"config": self.expected}, {"error": "load failed"}):
            with self.subTest(message=message), self.assertRaises(ValueError):
                self.evaluate([message])

    def test_truncated_output_fails(self):
        text = "\n".join(json.dumps(value) for value in self.messages)
        with self.assertRaises(ValueError):
            vulncheck.evaluate(text + '\n{"finding":', self.expected, self.packages)

    def test_missing_or_wrong_sbom_fails(self):
        for sbom in (None, {"go_version": "go1.27.0"}):
            self.messages = [{"config": self.expected}]
            if sbom is not None:
                self.messages.append({"SBOM": sbom})
            with self.subTest(sbom=sbom), self.assertRaises(ValueError):
                self.evaluate()

    def test_finding_requires_trace_and_advisory(self):
        for finding in (
            {"osv": "GO-2099-0001", "trace": []},
            {"osv": "GO-2099-0001", "trace": [{"module": "example.invalid/module"}]},
        ):
            with self.subTest(finding=finding), self.assertRaises(ValueError):
                self.evaluate([{"finding": finding}])

    def test_goenv_off_reports_an_empty_filename(self):
        environment = {"GOENV": "", "GOTOOLCHAIN": "local", "GOWORK": "off", "GOFLAGS": "-mod=readonly -p=1"}
        vulncheck.validate_environment(environment, "off")
        for requested, reported in ((None, ""), ("off", "off"), ("off", "/fixture/go.env")):
            with self.subTest(requested=requested, reported=reported), self.assertRaises(ValueError):
                vulncheck.validate_environment({**environment, "GOENV": reported}, requested)
        for key, value in (("GOTOOLCHAIN", "auto"), ("GOWORK", "/fixture/go.work"), ("GOFLAGS", "-tags=fixture")):
            with self.subTest(key=key), self.assertRaises(ValueError):
                vulncheck.validate_environment({**environment, key: value}, "off")

    def test_scan_roots_must_match_loaded_packages(self):
        for roots in ([], ["example.invalid/other"], self.packages["roots"] * 2):
            self.messages[1]["SBOM"]["roots"] = roots
            with self.subTest(roots=roots), self.assertRaises(ValueError):
                self.evaluate()

    def test_scan_modules_must_match_loaded_versions(self):
        modules = self.messages[1]["SBOM"]["modules"]
        for scanned in ([], modules[:-1], modules * 2, [{"path": "stdlib", "version": "v1.27.0"}, modules[0]]):
            self.messages[1]["SBOM"]["modules"] = scanned
            with self.subTest(modules=scanned), self.assertRaises(ValueError):
                self.evaluate()

    def test_expected_modules_use_loaded_not_unused_selected_dependencies(self):
        root = {"Path": "example.invalid/app", "Main": True}
        dependency = {"Path": "example.invalid/dependency", "Version": "v1.2.3"}
        selected = [root, dependency, {"Path": "example.invalid/unused", "Version": "v1.0.0"}]
        loaded = [
            {"ImportPath": "fmt", "DepOnly": True},
            {"ImportPath": "example.invalid/dependency/pkg", "DepOnly": True, "Module": dependency},
            {"ImportPath": "example.invalid/app", "Module": root},
        ]
        result = vulncheck.expected_sbom("\n".join(map(json.dumps, loaded)), selected, "go1.27.1")
        self.assertEqual(result, {
            "roots": ["example.invalid/app"],
            "modules": {"stdlib": "v1.27.1", "example.invalid/app": "", "example.invalid/dependency": "v1.2.3"},
        })
        for packages in ([], [{"Error": {"Err": "load failed"}}], [
            {"ImportPath": "example.invalid/app", "Module": {**root, "Version": "v9.0.0"}},
        ]):
            with self.subTest(packages=packages), self.assertRaises(ValueError):
                vulncheck.expected_sbom("\n".join(map(json.dumps, packages)), selected, "go1.27.1")


if __name__ == "__main__":
    unittest.main()
