package manager

import (
	"strings"
	"testing"
)

// realFormatDump is laid out the way a v16 `bench backup` writes it, as read
// from a live bench: a frappe metadata block first, the sandbox-mode comment,
// and an INSERT whose VALUES list starts on the next line with one row per line.
func realFormatDump(rows ...string) string {
	return "-- begin frappe metadata\n-- [frappe]\n-- version = 16.51.1\n-- branch = version-16\n-- end frappe metadata\n-- \n" +
		"/*M!999999\\- enable the sandbox mode */ \n-- MariaDB dump 10.19-11.8.6-MariaDB, for debian-linux-gnu (x86_64)\n--\n" +
		"-- Host: mariadb    Database: _33a16de3362e27ef\n\n" +
		mariaAuth + mariaUser +
		"--\n-- Table structure for table `tabInstalled Application`\n--\n\nDROP TABLE IF EXISTS `tabInstalled Application`;\n" +
		"/*!40101 SET @saved_cs_client     = @@character_set_client */;\n" +
		"CREATE TABLE `tabInstalled Application` (\n  `name` varchar(140) NOT NULL,\n  `creation` datetime(6) DEFAULT NULL,\n" +
		"  `modified` datetime(6) DEFAULT NULL,\n  `modified_by` varchar(140) DEFAULT NULL,\n  `owner` varchar(140) DEFAULT NULL,\n" +
		"  `docstatus` tinyint(4) NOT NULL DEFAULT 0,\n  `idx` int(11) NOT NULL DEFAULT 0,\n  `app_name` varchar(140) DEFAULT NULL,\n" +
		"  `app_version` varchar(140) DEFAULT NULL,\n  `git_branch` varchar(140) DEFAULT NULL,\n  `has_setup_wizard` tinyint(4) NOT NULL DEFAULT 0,\n" +
		"  `is_setup_complete` tinyint(4) NOT NULL DEFAULT 0,\n  `parent` varchar(140) DEFAULT NULL,\n  `parentfield` varchar(140) DEFAULT NULL,\n" +
		"  `parenttype` varchar(140) DEFAULT NULL,\n  PRIMARY KEY (`name`),\n  KEY `parent` (`parent`)\n) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;\n" +
		"/*!40101 SET character_set_client = @saved_cs_client */;\n\nSET @OLD_AUTOCOMMIT=@@AUTOCOMMIT, @@AUTOCOMMIT=0;\n" +
		"LOCK TABLES `tabInstalled Application` WRITE;\n/*!40000 ALTER TABLE `tabInstalled Application` DISABLE KEYS */;\n" +
		"INSERT INTO `tabInstalled Application` VALUES\n" + strings.Join(rows, ",\n") + ";\n" +
		"/*!40000 ALTER TABLE `tabInstalled Application` ENABLE KEYS */;\nUNLOCK TABLES;\n"
}

func realRow(id, app, ver, branch string) string {
	return "('" + id + "','2026-10-13 20:27:47.123241','2026-10-13 20:27:47.123241','Administrator','Administrator',0,1,'" +
		app + "','" + ver + "','" + branch + "',1,0,'Installed Applications','installed_applications','Installed Applications')"
}

func TestInspectDumpRealLayout(t *testing.T) {
	d := realFormatDump(
		realRow("h9f7m3eio4", "frappe", "16.51.1", "version-16"),
		realRow("h9f7m3eio5", "erpnext", "16.2.0", "version-16"),
	)
	info, err := inspectDump(strings.NewReader(d))
	if err != nil {
		t.Fatal(err)
	}
	if info.Engine != "mariadb" || !info.HasAuth || !info.HasUser || len(info.Apps) != 2 {
		t.Fatalf("info = %+v", info)
	}
	if info.Apps[1].Name != "erpnext" || info.Apps[1].Version != "16.2.0" {
		t.Errorf("apps = %+v", info.Apps)
	}
	if info.MetaVersion != "16.51.1" || info.MetaBranch != "version-16" || info.FrappeBranch() != "version-16" || info.FrappeVersion() != "16.51.1" {
		t.Errorf("metadata: %+v", info)
	}
}

func TestInspectDumpMetadataIsTheFallback(t *testing.T) {
	// No installed-apps rows version: the metadata block still names frappe.
	d := realFormatDump(realRow("a", "frappe", "", ""))
	info, err := inspectDump(strings.NewReader(d))
	if err != nil {
		t.Fatal(err)
	}
	if info.FrappeVersion() != "16.51.1" || info.FrappeBranch() != "version-16" {
		t.Errorf("%+v", info)
	}
}

func TestInspectDumpUnterminatedInsert(t *testing.T) {
	// An INSERT that never ends must not be read forever or accepted.
	d := "CREATE TABLE `tabInstalled Application` (\n  `app_name` varchar(1)\n);\nINSERT INTO `tabInstalled Application` VALUES\n('frappe')\n"
	info, err := inspectDump(strings.NewReader(d))
	if err != nil {
		t.Fatal(err)
	}
	if len(info.Apps) != 0 {
		t.Errorf("an unterminated INSERT produced apps: %+v", info.Apps)
	}
}
